// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// Copyright 2020 Opsdis AB

package main

import (
	"strings"

	"github.com/spf13/viper"
)

const (
	// Name of the program
	ExporterName = "aci-streamer"

	// MetricsPrefix the prefix for all internal metrics
	MetricsPrefix = "aci_streamer_"
)

// ExporterNameAsEnv return the ExportName as an env prefix
func ExporterNameAsEnv() string {
	return strings.ToUpper(strings.ReplaceAll(ExporterName, "-", "_"))
}

// SetDefaultValues define all default values
func SetDefaultValues() {

	// If set as env vars use the ExporterName as prefix like ACI_STREAMER_PORT for the port var
	viper.SetEnvPrefix(ExporterNameAsEnv())

	// All fields with . will be replaced with _ for ENV vars
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// aci-streamer
	viper.SetDefault("port", 9644)
	viper.BindEnv("port")
	viper.SetDefault("logfile", "")
	viper.BindEnv("logfile")
	viper.SetDefault("logformat", "json")
	viper.BindEnv("logformat")
	viper.SetDefault("config", "config")
	viper.BindEnv("config")
	viper.SetDefault("fabric", "")
	viper.BindEnv("fabric")

	// HTTPCLient
	viper.SetDefault("HTTPClient.timeout", 3)
	viper.BindEnv("HTTPClient.timeout")

	viper.SetDefault("HTTPClient.keepalive", 10)
	viper.BindEnv("HTTPClient.keepalive")

	viper.SetDefault("HTTPClient.tlshandshaketimeout", 10)
	viper.BindEnv("HTTPClient.tlshandshaketimeout")

	viper.SetDefault("HTTPClient.insecureHTTPS", true)
	viper.BindEnv("HTTPClient.insecureHTTPS")
}
