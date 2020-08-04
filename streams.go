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

type Streams map[string]*Stream
type Stream struct {
	ClassName      string         `mapstructure:"class_name"`
	QueryParameter string         `mapstructure:"query_parameter"`
	Root           string         `mapstructure:"root"`
	Labels         []ConfigLabels `string:"labels"`
	Timestamp      Timestamp      `mapstructure:"timestamp"`
	Message        Message        `string:"message"`
}

type ConfigLabels struct {
	PropertyName string `mapstructure:"property_name"`
	Regex        string `mapstructure:"regex"`
}
type Timestamp struct {
	PropertyName string `mapstructure:"property_name"`
	//Regex        string `mapstructure:"regex"`
}

type Message struct {
	Name       string   `mapstructure:"name"`
	Format     string   `mapstructure:"format"`
	Properties []string `mapstructure:"property_names"`
}
