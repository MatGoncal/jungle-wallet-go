package main

import (
	"github.com/matheusgoncalves/jungle-wallet-go/internal/appfx"
	"go.uber.org/fx"
)

func main() {
	fx.New(appfx.Options()).Run()
}
