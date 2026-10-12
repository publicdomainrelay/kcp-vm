package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"swproof/busdial"
	"swproof/startunit"
)

type kv map[string]string

func (m kv) String() string { return fmt.Sprint(map[string]string(m)) }
func (m kv) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("want k=v")
	}
	m[k] = v
	return nil
}

func main() {
	mode := flag.String("mode", "private", "session|private")
	unit := flag.String("unit", "", "unit name")
	props := kv{}
	flag.Var(props, "p", "property k=v")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := busdial.Systemd(ctx, *mode, "")
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(2)
	}
	defer c.Close()
	r, err := startunit.Start(ctx, c, *unit, flag.Args(), props)
	fmt.Printf("start %s result=%q err=%v\n", *unit, r, err)
	if err != nil {
		os.Exit(1)
	}
}
