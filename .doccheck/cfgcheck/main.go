package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/makkalot/caretz/internal/config"
)

// Check every JSON config example from the docs against the real config type.
//
// Go's decoder ignores unknown keys by default, so DisallowUnknownFields is the
// only way to catch docs that invent settings the runtime never reads. Load() is
// then used to exercise the same validation the CLI performs at startup.
func main() {
	dir := os.Args[1]
	// Optional: files listed here are not Caretz's own config schema.
	external := map[string]bool{}
	for _, a := range os.Args[2:] {
		external[a] = true
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	bad := 0
	for _, f := range files {
		base := filepath.Base(f)
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("  ERROR %-38s %v\n", base, err)
			bad++
			continue
		}
		if !json.Valid(data) {
			var probe any
			err := json.Unmarshal(data, &probe)
			fmt.Printf("  FAIL  %-38s invalid JSON: %v\n", base, err)
			bad++
			continue
		}
		if external[base] {
			fmt.Printf("  PASS  %-38s (third-party schema, JSON valid)\n", base)
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var cfg config.Config
		if err := dec.Decode(&cfg); err != nil {
			fmt.Printf("  FAIL  %-38s unknown/invalid key: %v\n", base, err)
			bad++
			continue
		}
		tmp := filepath.Join(os.TempDir(), "doccheck-cfg")
		_ = os.MkdirAll(tmp, 0o755)
		dest := filepath.Join(tmp, base)
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			fmt.Printf("  ERROR %-38s %v\n", base, err)
			bad++
			continue
		}
		loaded, err := config.LoadProfileWithTrust("", dest, "", true)
		if err != nil {
			fmt.Printf("  FAIL  %-38s config.Load: %v\n", base, err)
			bad++
			continue
		}
		// Provider examples may rely on startup /models discovery instead of a
		// static models block, so resolve again with a discovered catalog.
		res, err := loaded.Resolve()
		if err != nil {
			discovered := map[string][]string{}
			for key, provider := range loaded.Provider {
				if strings.Contains(cfg.ActiveModel, "/") && strings.HasPrefix(cfg.ActiveModel, key+"/") {
					discovered[key] = []string{strings.SplitN(cfg.ActiveModel, "/", 2)[1]}
				}
				for _, m := range provider.Models {
					_ = m
				}
			}
			withDisc := loaded.WithDiscoveredModels(discovered)
			res2, err2 := withDisc.Resolve()
			if err2 != nil {
				fmt.Printf("  FAIL  %-38s Resolve: %v (also fails with discovery: %v)\n", base, err, err2)
				bad++
				continue
			}
			fmt.Printf("  PASS* %-38s activeModel=%-18s resolves only via model discovery: model=%q ctx=%d\n", base,
				cfg.ActiveModel, orNA(res2.Model), res2.ContextWindow)
			continue
		}
		fmt.Printf("  PASS  %-38s activeModel=%-18s resolved model=%q ctx=%d\n", base,
			cfg.ActiveModel, orNA(res.Model), res.ContextWindow)
	}
	fmt.Printf("\n%d failing config example(s)\n", bad)
	if bad > 0 {
		os.Exit(1)
	}
}

func orNA(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "<none>"
	}
	return s
}
