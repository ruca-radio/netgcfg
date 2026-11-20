package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ruca-radio/netgcfg/cfg"
	"github.com/ruca-radio/netgcfg/rax"
)

const openIssueMsg = `
Please open a bug report at https://github.com/ruca-radio/netgcfg/issues.
Include the exact command that failed, the error message, and the the model and firmware version of your device.`

const usageText = `Usage: netgcfg <command> [options]

Commands:
  decrypt   decrypt a .cfg backup to JSON       netgcfg decrypt -in FILE -out FILE
  encrypt   re-encode JSON back to a .cfg       netgcfg encrypt -in FILE -out FILE
  tui       interactive load/edit/save/export   netgcfg tui

Options:
  -in FILE    input file (required for decrypt/encrypt)
  -out FILE   output file (required for decrypt/encrypt)
  -raw        decrypt to a Base64 config_raw field instead of a JSON tree

With no command, netgcfg launches the interactive TUI.`

// options holds the parsed command line.
type options struct {
	command string
	in      string
	out     string
	raw     bool
}

// parseArgs parses a command plus interleaved flags. It fixes the standard
// library flag package's habit of treating the value after a string flag as a
// flag (e.g. `-decrypt -raw FILE`), so flags may appear in any order and
// `-decrypt FILE` (legacy style) still works.
func parseArgs(args []string) (*options, error) {
	o := &options{}
	i := 0

	// A leading bare word is a command.
	if i < len(args) && !strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "decrypt", "encrypt", "tui":
			o.command = args[i]
			i++
		case "help":
			return nil, fmt.Errorf("help")
		default:
			return nil, fmt.Errorf("unknown command %q", args[i])
		}
	}

	for i < len(args) {
		arg := args[i]
		i++
		if arg == "" {
			continue
		}
		// A bare non-flag token is a positional input file.
		if !strings.HasPrefix(arg, "-") {
			if o.in != "" {
				return nil, fmt.Errorf("unexpected argument %q", arg)
			}
			o.in = arg
			continue
		}

		// Split -flag / --flag / -flag=value.
		name := strings.TrimLeft(arg, "-")
		value := ""
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			value = name[eq+1:]
			name = name[:eq]
			hasValue = true
		}

		// consume returns the flag's value. Unlike the stdlib flag package it
		// refuses to swallow a following token that looks like another flag,
		// which is what broke `-decrypt -raw FILE`.
		consume := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i >= len(args) || strings.HasPrefix(args[i], "-") {
				return "", fmt.Errorf("flag -%s needs a value", name)
			}
			v := args[i]
			i++
			return v, nil
		}

		switch name {
		case "in", "input":
			v, err := consume()
			if err != nil {
				return nil, err
			}
			o.in = v
		case "out", "output":
			v, err := consume()
			if err != nil {
				return nil, err
			}
			o.out = v
		case "raw":
			o.raw = true
		case "decrypt", "encrypt", "tui":
			if o.command != "" && o.command != name {
				return nil, fmt.Errorf("multiple commands: %q and %q", o.command, name)
			}
			o.command = name
			// Legacy style: `-decrypt FILE` consumes the next non-flag token
			// as the input file.
			if !hasValue && i < len(args) && !strings.HasPrefix(args[i], "-") {
				o.in = args[i]
				i++
			}
		case "h", "help":
			return nil, fmt.Errorf("help")
		default:
			return nil, fmt.Errorf("unknown flag %q", arg)
		}
	}

	// No command and no input: launch the TUI. No command but an input file is
	// ambiguous — ask for an explicit command.
	if o.command == "" {
		if o.in == "" {
			o.command = "tui"
		} else {
			return nil, fmt.Errorf("specify decrypt or encrypt")
		}
	}
	return o, nil
}

func main() {
	l := log.New(os.Stderr, "", 0)

	o, err := parseArgs(os.Args[1:])
	if err != nil {
		if err.Error() == "help" {
			fmt.Fprintln(os.Stderr, usageText)
			os.Exit(0)
		}
		l.Println(err)
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}

	switch o.command {
	case "", "tui":
		runTUI(l)
	case "decrypt":
		runDecrypt(l, o)
	case "encrypt":
		runEncrypt(l, o)
	default:
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}
}

func runDecrypt(l *log.Logger, o *options) {
	if o.in == "" {
		l.Println("decrypt needs an input file (-in)")
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}
	if o.out == "" {
		l.Println("decrypt needs an output file (-out)")
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}

	b, err := os.ReadFile(o.in)
	if err != nil {
		l.Fatal(err)
	}

	var wrapperJSON []byte
	if rax.IsRAXConfig(b) {
		c, err := rax.Decrypt(b)
		if err != nil {
			l.Println("decrypt config:", err)
			l.Fatalln(openIssueMsg)
		}
		if wrapperJSON, err = c.ToJSON(o.raw); err != nil {
			l.Println("create json wrapper:", err)
			l.Fatalln(openIssueMsg)
		}
	} else {
		_, configBytes, metadata, err := cfg.Decrypt(b)
		if err != nil {
			l.Println("decrypt config:", err)
			l.Fatalln(openIssueMsg)
		}
		if wrapperJSON, err = cfg.ToJSON(configBytes, metadata, o.raw); err != nil {
			l.Println("create json wrapper:", err)
			l.Fatalln(openIssueMsg)
		}
	}

	if err := writeFileNoTrunc(o.out, wrapperJSON); err != nil {
		l.Fatal(err)
	}
}

func runEncrypt(l *log.Logger, o *options) {
	if o.in == "" {
		l.Println("encrypt needs an input file (-in)")
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}
	if o.out == "" {
		l.Println("encrypt needs an output file (-out)")
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(1)
	}

	wrapperJSON, err := os.ReadFile(o.in)
	if err != nil {
		l.Fatal(err)
	}

	var encryptedConfig []byte
	if isRAXJSON(wrapperJSON) {
		c, err := rax.FromJSON(wrapperJSON)
		if err != nil {
			l.Fatalln("parse json wrapper:", err)
		}
		if encryptedConfig, err = c.Encrypt(); err != nil {
			l.Println("encrypt config:", err)
			l.Fatalln(openIssueMsg)
		}
	} else {
		configBytes, metadata, err := cfg.FromJSON(wrapperJSON)
		if err != nil {
			l.Fatalln("parse json wrapper:", err)
		}
		if encryptedConfig, err = cfg.Encrypt(configBytes, metadata); err != nil {
			l.Println("encrypt config:", err)
			l.Fatalln(openIssueMsg)
		}
	}

	if err := writeFileNoTrunc(o.out, encryptedConfig); err != nil {
		l.Fatal(err)
	}
}

// isRAXJSON reports whether a JSON wrapper was produced by the RAX/Broadcom
// codec (identified by metadata.format).
func isRAXJSON(b []byte) bool {
	var probe struct {
		Metadata *struct {
			Format string `json:"format"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(b, &probe); err != nil || probe.Metadata == nil {
		return false
	}
	return probe.Metadata.Format == rax.FormatName
}

func writeFileNoTrunc(name string, b []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Close()
}
