// Copyright 2017 fatedier, fatedier@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"fmt"
	"io/ioutil"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatedier/frp/g"
	"github.com/fatedier/frp/models/config"
	"github.com/fatedier/frp/models/msg"
	"github.com/fatedier/frp/utils/log"
	frpNet "github.com/fatedier/frp/utils/net"
	"github.com/fatedier/frp/utils/util"
	"github.com/fatedier/frp/utils/version"

	fmux "github.com/hashicorp/yamux"
)

type Service struct {
	// uniq id got from frps, attach it in loginMsg
	runId string

	// manager control connection with server
	ctl   *Control
	ctlMu sync.RWMutex

	pxyCfgs     map[string]config.ProxyConf
	visitorCfgs map[string]config.VisitorConf
	cfgMu       sync.RWMutex

	exit     uint32 // 0 means not exit
	closedCh chan bool

	closed bool
}

func NewService(pxyCfgs map[string]config.ProxyConf, visitorCfgs map[string]config.VisitorConf) (svr *Service) {
	svr = &Service{
		pxyCfgs:     pxyCfgs,
		visitorCfgs: visitorCfgs,
		exit:        0,
		closedCh:    make(chan bool),
		closed:      true,
	}
	return
}

func (svr *Service) GetController() *Control {
	svr.ctlMu.RLock()
	defer svr.ctlMu.RUnlock()
	return svr.ctl
}

func (svr *Service) Run(cmd bool) error {
	// first login
	for {
		conn, session, err := svr.login()
		if err != nil {
			log.Warn("login to server failed: %v", err)

			// if login_fail_exit is true, just exit this program
			// otherwise sleep a while and try again to connect to server
			if g.GlbClientCfg.LoginFailExit {
				return err
			} else {
				time.Sleep(10 * time.Second)
			}
		} else {
			// login success
			ctl := NewControl(svr.runId, conn, session, svr.pxyCfgs, svr.visitorCfgs)
			ctl.Run()
			svr.ctlMu.Lock()
			svr.ctl = ctl
			svr.ctlMu.Unlock()
			break
		}
	}

	go svr.keepControllerWorking()

	if g.GlbClientCfg.AdminPort != 0 {
		err := svr.RunAdminServer(g.GlbClientCfg.AdminAddr, g.GlbClientCfg.AdminPort)
		if err != nil {
			log.Warn("run admin server error: %v", err)
		}
		log.Info("admin server listen on %s:%d", g.GlbClientCfg.AdminAddr, g.GlbClientCfg.AdminPort)
	}

	svr.closed = false
	if cmd {
		svr.closed = <-svr.closedCh
		log.Info("svr closed")
	} else {
		go func() {
			svr.closed = <-svr.closedCh
			log.Info("svr closed")
		}()
	}
	return nil
}

func (svr *Service) keepControllerWorking() {
	maxDelayTime := 20 * time.Second
	delayTime := time.Second

	for {
		<-svr.ctl.ClosedDoneCh()
		if atomic.LoadUint32(&svr.exit) != 0 {
			return
		}

		for {
			log.Info("try to reconnect to server...")
			conn, session, err := svr.login()
			if err != nil {
				log.Warn("reconnect to server error: %v", err)
				time.Sleep(delayTime)
				delayTime = delayTime * 2
				if delayTime > maxDelayTime {
					delayTime = maxDelayTime
				}
				continue
			}
			// reconnect success, init delayTime
			delayTime = time.Second

			ctl := NewControl(svr.runId, conn, session, svr.pxyCfgs, svr.visitorCfgs)
			ctl.Run()
			svr.ctlMu.Lock()
			svr.ctl = ctl
			svr.ctlMu.Unlock()
			break
		}
	}
}

// login creates a connection to frps and registers it self as a client
// conn: control connection
// session: if it's not nil, using tcp mux
func (svr *Service) login() (conn frpNet.Conn, session *fmux.Session, err error) {
	conn, err = frpNet.ConnectServerByProxy(g.GlbClientCfg.HttpProxy, g.GlbClientCfg.Protocol,
		newAddress(g.GlbClientCfg.ServerAddr, g.GlbClientCfg.ServerPort))
	if err != nil {
		return
	}

	defer func() {
		if err != nil {
			conn.Close()
		}
	}()

	if g.GlbClientCfg.TcpMux {
		fmuxCfg := fmux.DefaultConfig()
		fmuxCfg.KeepAliveInterval = 20 * time.Second
		fmuxCfg.LogOutput = ioutil.Discard
		session, err = fmux.Client(conn, fmuxCfg)
		if err != nil {
			return
		}
		stream, errRet := session.OpenStream()
		if errRet != nil {
			session.Close()
			err = errRet
			return
		}
		conn = frpNet.WrapConn(stream)
	}

	now := time.Now().Unix()
	loginMsg := &msg.Login{
		Arch:         runtime.GOARCH,
		Os:           runtime.GOOS,
		PoolCount:    g.GlbClientCfg.PoolCount,
		User:         g.GlbClientCfg.User,
		Version:      version.Full(),
		PrivilegeKey: util.GetAuthKey(g.GlbClientCfg.Token, now),
		Timestamp:    now,
		RunId:        svr.runId,
	}

	if err = msg.WriteMsg(conn, loginMsg); err != nil {
		return
	}

	var loginRespMsg msg.LoginResp
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err = msg.ReadMsgInto(conn, &loginRespMsg); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})

	if loginRespMsg.Error != "" {
		err = fmt.Errorf("%s", loginRespMsg.Error)
		log.Error("%s", loginRespMsg.Error)
		return
	}

	svr.runId = loginRespMsg.RunId
	g.GlbClientCfg.ServerUdpPort = loginRespMsg.ServerUdpPort
	log.Info("login to server success, get run id [%s], server udp port [%d]", loginRespMsg.RunId, loginRespMsg.ServerUdpPort)
	return
}

func (svr *Service) ReloadConf(pxyCfgs map[string]config.ProxyConf, visitorCfgs map[string]config.VisitorConf) error {
	svr.cfgMu.Lock()
	svr.pxyCfgs = pxyCfgs
	svr.visitorCfgs = visitorCfgs
	svr.cfgMu.Unlock()

	return svr.ctl.ReloadConf(pxyCfgs, visitorCfgs)
}

func (svr *Service) Close() {
	atomic.StoreUint32(&svr.exit, 1)
	if svr.ctl != nil {
		svr.ctl.Close()
	}
	svr.closedCh <- true
}

func (svr *Service) IsClosed() bool {
	return svr.closed
}
