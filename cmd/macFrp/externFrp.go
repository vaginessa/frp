package main

/*
 */
import "C"
import (
	"github.com/fatedier/frp_0290/cmd/frpc/sub"
	"github.com/fatedier/frp_0290/cmd/frps/frps"
	"github.com/fatedier/golib/crypto"
)

//export RunFrps
func RunFrps(cfgFilePath *C.char) C.int {
	path := C.GoString(cfgFilePath)
	crypto.DefaultSalt = "frp"

	if err := frps.RunFrps(path); err != nil {
		println(err.Error())
		return C.int(0)
	}
	return C.int(1)
}

//export RunFrpc
func RunFrpc(cfgFilePath *C.char) C.int {
	path := C.GoString(cfgFilePath)
	crypto.DefaultSalt = "frp"

	if err := sub.RunFrpc(path); err != nil {
		println(err.Error())
		return C.int(0)
	}
	return C.int(1)
}
