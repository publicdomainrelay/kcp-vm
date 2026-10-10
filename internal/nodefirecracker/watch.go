package nodefirecracker

import "github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"

func (h *Host) Terminal() string { return nodevm.Fatal(h.opts.DataDir) }

func writeNamespaceRecord(paramsPath string) error { return nodevm.WriteNamespaceRecord(paramsPath) }

func stripJournalPrefix(line string) string { return nodevm.StripJournalPrefix(line) }
