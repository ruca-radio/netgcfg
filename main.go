package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/ruca-radio/netgcfg/cfg"
	"github.com/ruca-radio/netgcfg/rax"
)

const openIssueMsg = `
Please open a bug report at https://github.com/ruca-radio/netgcfg/issues.
Include the exact command that failed, the error message, and the the model and firmware version of your device.`

func main() {
	l := log.New(os.Stderr, "", 0)

	decryptFile := flag.String("decrypt", "", "file to decrypt (requires: -out)")
	encryptFile := flag.String("encrypt", "", "file to encrypt (requires: -out, -magic)")
	raw := flag.Bool("raw", false, "decrypt the raw bytes to a Base64-encoded field")
	outputFile := flag.String("out", "", "output file for decryption or encryption")
	flag.Parse()

	if *decryptFile != "" {
		if *outputFile == "" {
			l.Println("-decrypt needs an output file")
			flag.Usage()
			os.Exit(1)
		}

		b, err := os.ReadFile(*decryptFile)
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
			if wrapperJSON, err = c.ToJSON(*raw); err != nil {
				l.Println("create json wrapper:", err)
				l.Fatalln(openIssueMsg)
			}
		} else {
			_, configBytes, metadata, err := cfg.Decrypt(b)
			if err != nil {
				l.Println("decrypt config:", err)
				l.Fatalln(openIssueMsg)
			}
			if wrapperJSON, err = cfg.ToJSON(configBytes, metadata, *raw); err != nil {
				l.Println("create json wrapper:", err)
				l.Fatalln(openIssueMsg)
			}
		}

		if err := writeFileNoTrunc(*outputFile, wrapperJSON); err != nil {
			l.Fatal(err)
		}
	} else if *encryptFile != "" {
		if *outputFile == "" {
			l.Println("-encrypt needs an output file")
			flag.Usage()
			os.Exit(1)
		}

		wrapperJSON, err := os.ReadFile(*encryptFile)
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

		if err := writeFileNoTrunc(*outputFile, encryptedConfig); err != nil {
			l.Fatal(err)
		}
	} else {
		flag.Usage()
		os.Exit(1)
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
