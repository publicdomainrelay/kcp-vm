//go:build !release

package main

import "github.com/johnandersen777/socialweb-computer-kcp/internal/nodeimagemain"

func devFoldedVerbs() map[string]foldedVerb {
	return map[string]foldedVerb{
		"nodeimage guest-rootfs":   {[]string{nodeimagemain.ModeGuestRootfs}, nodeimagemain.Main},
		"nodeimage guest-manifest": {[]string{nodeimagemain.ModeGuestManifest}, nodeimagemain.Main},
	}
}
