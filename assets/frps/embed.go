package frpc

import (
	"embed"

	"github.com/HaidyCao/frp_0420/assets"
)

//go:embed static/*
var content embed.FS

func init() {
	assets.Register(content)
}
