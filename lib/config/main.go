package config

import (
	"os"
	"strings"
)

var TraktClientId = getConfig("TRAKT_ID")
var TraktClientSecret = getConfig("TRAKT_SECRET")

func getConfig(name string) string {
	if os.Getenv(name) != "" {
		return os.Getenv(name)
	} else if os.Getenv(name+"_FILE") != "" {
		file, err := os.ReadFile(os.Getenv(name + "_FILE"))

		if err != nil {
			panic(err)
		}

		return strings.TrimSpace(string(file))
	}

	return ""
}
