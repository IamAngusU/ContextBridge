package main

import (
	"flag"
	"strings"
)

// parseInterspersedFlags preserves the documented CLI shape where flags may
// appear before or after positional IDs. The standard flag package stops at
// the first positional argument, so commands such as `result JOB --config X`
// otherwise misclassify the trailing option as another positional argument.
// Known options and their values are moved before an explicit -- boundary;
// unknown options are still passed to flag.FlagSet and fail normally.
func parseInterspersedFlags(flags *flag.FlagSet, args []string) error {
	options := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	optionsEnabled := true
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if optionsEnabled && argument == "--" {
			optionsEnabled = false
			continue
		}
		if !optionsEnabled || argument == "-" || !strings.HasPrefix(argument, "-") {
			positionals = append(positionals, argument)
			continue
		}

		name := strings.TrimLeft(argument, "-")
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			name = name[:separator]
			options = append(options, argument)
			continue
		}
		option := flags.Lookup(name)
		options = append(options, argument)
		if option == nil || isBooleanFlag(option) {
			continue
		}
		if index+1 < len(args) {
			index++
			options = append(options, args[index])
		}
	}
	if len(positionals) > 0 {
		options = append(options, "--")
		options = append(options, positionals...)
	}
	return flags.Parse(options)
}

func isBooleanFlag(option *flag.Flag) bool {
	type booleanFlag interface {
		IsBoolFlag() bool
	}
	value, ok := option.Value.(booleanFlag)
	return ok && value.IsBoolFlag()
}
