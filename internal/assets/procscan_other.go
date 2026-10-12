//go:build !linux

package assets

func ProcessesOfUID(uid int, match func(argv []string) bool) []int { return nil }
