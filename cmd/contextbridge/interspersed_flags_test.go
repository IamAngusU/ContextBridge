package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterspersedFlagsAcceptsOptionsAroundPositionals(t *testing.T) {
	for _, arguments := range [][]string{
		{"--config", "config.yml", "job-1", "--json"},
		{"job-1", "--config", "config.yml", "--json"},
		{"--json", "job-1", "--config=config.yml"},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		configPath := flags.String("config", "default.yml", "")
		asJSON := flags.Bool("json", false, "")
		if err := parseInterspersedFlags(flags, arguments); err != nil {
			t.Fatalf("%#v: %v", arguments, err)
		}
		if *configPath != "config.yml" || !*asJSON || !reflect.DeepEqual(flags.Args(), []string{"job-1"}) {
			t.Fatalf("unexpected parse for %#v: config=%q json=%t args=%#v", arguments, *configPath, *asJSON, flags.Args())
		}
	}
}

func TestParseInterspersedFlagsHonorsDoubleDash(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	configPath := flags.String("config", "default.yml", "")
	if err := parseInterspersedFlags(flags, []string{"--config", "real.yml", "--", "--config", "literal"}); err != nil {
		t.Fatal(err)
	}
	if *configPath != "real.yml" || !reflect.DeepEqual(flags.Args(), []string{"--config", "literal"}) {
		t.Fatalf("double-dash boundary was lost: config=%q args=%#v", *configPath, flags.Args())
	}
}

func TestParseInterspersedFlagsStillRejectsUnknownOption(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	if err := parseInterspersedFlags(flags, []string{"job-1", "--unknown"}); err == nil {
		t.Fatal("unknown option was accepted")
	}
}
