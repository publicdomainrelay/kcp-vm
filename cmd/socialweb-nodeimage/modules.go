package main

import "github.com/johnandersen777/socialweb-computer-kcp/internal/kernelmodules"

func VerifyModules(cfg Config) error {
	return kernelmodules.Verify(cfg.ModuleSource, cfg.Modules)
}
