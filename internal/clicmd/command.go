// Package clicmd describes and parses small command trees.
package clicmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ArgsValidator func(*Command, []string) error
type RunFunc func(context.Context, *Result) int

type Command struct {
	Use                string
	Short              string
	Options            any
	InheritRootOptions bool
	Args               ArgsValidator
	Run                RunFunc
	parent             *Command
	children           []*Command
	defs               []optionDef
	schemaReady        bool
}

type ParsedOptions struct {
	Value   any
	changed map[string]bool
	set     []setFlag
}

type setFlag struct{ name, value string }

func (o ParsedOptions) Changed(name string) bool { return o.changed[name] }

// FlagArgs re-emits the flags that were set, in command-line order, as
// --name=value tokens, skipping the excluded names. Repeated flags stay
// repeated, so the result parses back to the same option values.
func (o ParsedOptions) FlagArgs(exclude ...string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	var args []string
	for _, flag := range o.set {
		if !skip[flag.name] {
			args = append(args, "--"+flag.name+"="+flag.value)
		}
	}
	return args
}

type Result struct {
	Command     *Command
	Args        []string
	Help        bool
	RootOptions ParsedOptions
	Options     ParsedOptions
}

type optionDef struct {
	name, help, group string
	value             flag.Value
	isBool            bool
	placeholder       string
	defaultText       string
	showDefault       bool
}

// AddCommand registers children and panics on invalid static definitions.
func (c *Command) AddCommand(children ...*Command) {
	if c == nil {
		panic("clicmd: nil parent command")
	}
	validateCommand(c)
	for _, child := range children {
		if child == nil || child.name() == "" || strings.ContainsAny(child.name(), " \t") {
			panic("clicmd: invalid child command name")
		}
		if child.parent != nil {
			panic(fmt.Sprintf("clicmd: command %q already has a parent", child.name()))
		}
		if c.child(child.name()) != nil {
			panic(fmt.Sprintf("clicmd: duplicate command %q", child.name()))
		}
		validateCommand(child)
		child.parent = c
		validateInheritedOptions(child)
		c.children = append(c.children, child)
	}
}

// Parse selects a command, populates its option structs, and validates positionals.
func (c *Command) Parse(args []string) (*Result, error) {
	validateTree(c)
	validateInheritedOptions(c)
	selected, commandIndexes, err := c.selectCommand(args)
	if err != nil {
		return nil, err
	}
	localDefs := commandDefs(selected)
	var rootDefs []optionDef
	if selected == c || selected.InheritRootOptions {
		rootDefs = commandDefs(c)
	}
	result := &Result{Command: selected, Options: ParsedOptions{Value: selected.Options, changed: map[string]bool{}}}
	if selected == c || selected.InheritRootOptions {
		result.RootOptions = ParsedOptions{Value: c.Options, changed: map[string]bool{}}
	}
	defs := make(map[string]optionDef, len(localDefs)+len(rootDefs))
	declaredAtRoot := make(map[string]bool)
	for _, def := range commandDefs(c) {
		declaredAtRoot[def.name] = true
	}
	lastCommandIndex := -1
	for index := range commandIndexes {
		if index > lastCommandIndex {
			lastCommandIndex = index
		}
	}
	owners := make(map[string]*ParsedOptions, len(localDefs)+len(rootDefs))
	for _, def := range rootDefs {
		defs[def.name] = def
		owners[def.name] = &result.RootOptions
	}
	for _, def := range localDefs {
		if _, exists := defs[def.name]; exists && selected != c {
			panic(fmt.Sprintf("clicmd: duplicate inherited flag %q", def.name))
		}
		defs[def.name] = def
		owners[def.name] = &result.Options
	}
	flagsEnabled := true
	for i := 0; i < len(args); i++ {
		if commandIndexes[i] {
			continue
		}
		arg := args[i]
		if flagsEnabled && arg == "--" {
			flagsEnabled = false
			continue
		}
		if flagsEnabled && (arg == "-h" || arg == "--help") {
			result.Help = true
			continue
		}
		if flagsEnabled && isFlag(arg) {
			name, value, hasValue, err := splitFlag(arg)
			if err != nil {
				return result, err
			}
			def, ok := defs[name]
			if !ok {
				if selected != c && !selected.InheritRootOptions && declaredAtRoot[name] && i < lastCommandIndex {
					return result, fmt.Errorf("--%s is not valid with subcommand %s", name, selected.commandPath())
				}
				return result, fmt.Errorf("flag provided but not defined: -%s", name)
			}
			if !hasValue {
				if def.isBool {
					value = "true"
				} else {
					if i+1 >= len(args) {
						return result, fmt.Errorf("flag needs an argument: -%s", name)
					}
					i++
					value = args[i]
				}
			}
			if err := def.value.Set(value); err != nil {
				return result, fmt.Errorf("invalid value %q for flag -%s: %w", value, name, err)
			}
			owners[name].changed[name] = true
			owners[name].set = append(owners[name].set, setFlag{name: name, value: value})
			continue
		}
		result.Args = append(result.Args, arg)
	}
	if !result.Help && selected.Args != nil {
		if err := selected.Args(selected, result.Args); err != nil {
			return result, err
		}
	}
	if len(selected.children) > 0 && selected.Args == nil && len(result.Args) == 0 {
		result.Help = true
	}
	if selected == c {
		result.RootOptions = result.Options
	}
	return result, nil
}

func (c *Command) selectCommand(args []string) (*Command, map[int]bool, error) {
	all := make(map[string]bool)
	collectAllOptions(c, all)
	current := c
	indexes := make(map[int]bool)
	flagsEnabled, positional := true, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if flagsEnabled && arg == "--" {
			flagsEnabled = false
			continue
		}
		if flagsEnabled && (arg == "-h" || arg == "--help") {
			continue
		}
		if flagsEnabled && isFlag(arg) {
			name, _, hasValue, err := splitFlag(arg)
			if err == nil {
				if isBool, known := all[name]; known && !isBool && !hasValue && i+1 < len(args) {
					i++
				}
			}
			continue
		}
		if !positional {
			if child := current.child(arg); child != nil {
				current = child
				indexes[i] = true
				continue
			}
			if len(current.children) > 0 && current.Args == nil {
				return nil, nil, fmt.Errorf("unknown command %q for %s", arg, current.commandPath())
			}
		}
		positional = true
	}
	return current, indexes, nil
}

func collectAllOptions(c *Command, result map[string]bool) {
	for _, def := range commandDefs(c) {
		result[def.name] = def.isBool
	}
	for _, child := range c.children {
		collectAllOptions(child, result)
	}
}

func splitFlag(arg string) (string, string, bool, error) {
	text := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	name, value, hasValue := strings.Cut(text, "=")
	if name == "" {
		return "", "", false, fmt.Errorf("invalid flag %q", arg)
	}
	return name, value, hasValue, nil
}
func isFlag(arg string) bool { return len(arg) > 1 && arg[0] == '-' && arg != "-" }

func validateTree(c *Command) {
	if c == nil {
		panic("clicmd: nil command")
	}
	validateCommand(c)
	seen := make(map[string]bool)
	for _, child := range c.children {
		if child == nil || child.parent != c || seen[child.name()] {
			panic("clicmd: invalid command tree")
		}
		seen[child.name()] = true
		validateTree(child)
	}
}
func validateCommand(c *Command) {
	if c == nil || c.name() == "" {
		panic("clicmd: command Use must contain a name")
	}
	_ = commandDefs(c)
}

func validateInheritedOptions(c *Command) {
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if c.InheritRootOptions && c != root {
		seen := make(map[string]bool)
		for _, def := range commandDefs(root) {
			seen[def.name] = true
		}
		for _, def := range commandDefs(c) {
			if seen[def.name] {
				panic(fmt.Sprintf("clicmd: duplicate inherited flag %q", def.name))
			}
		}
	}
	for _, child := range c.children {
		validateInheritedOptions(child)
	}
}

func commandDefs(c *Command) []optionDef {
	if !c.schemaReady {
		c.defs = optionDefs(c.Options)
		c.schemaReady = true
	}
	return c.defs
}

var flagValueType = reflect.TypeOf((*flag.Value)(nil)).Elem()
var durationType = reflect.TypeOf(time.Duration(0))

func optionDefs(options any) []optionDef {
	if options == nil {
		return nil
	}
	v := reflect.ValueOf(options)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		panic("clicmd: Options must be a non-nil pointer to a struct")
	}
	var defs []optionDef
	collectOptionDefs(v.Elem(), &defs, make(map[string]bool))
	return defs
}

func collectOptionDefs(v reflect.Value, defs *[]optionDef, seen map[string]bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		fieldType, field := t.Field(i), v.Field(i)
		name := fieldType.Tag.Get("flag")
		if name == "" && fieldType.Anonymous {
			if field.Kind() == reflect.Struct {
				collectOptionDefs(field, defs, seen)
				continue
			}
			if field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.Struct {
				if field.IsNil() {
					if !field.CanSet() {
						panic(fmt.Sprintf("clicmd: embedded options %s are not settable", fieldType.Name))
					}
					field.Set(reflect.New(field.Type().Elem()))
				}
				collectOptionDefs(field.Elem(), defs, seen)
				continue
			}
		}
		if name == "" {
			continue
		}
		if strings.TrimSpace(name) != name || strings.ContainsAny(name, "= \t") || seen[name] {
			panic(fmt.Sprintf("clicmd: invalid or duplicate flag %q", name))
		}
		value, isBool := valueFor(field, name)
		if text, ok := fieldType.Tag.Lookup("default"); ok {
			if err := value.Set(text); err != nil {
				panic(fmt.Sprintf("clicmd: invalid default %q for flag %q: %v", text, name, err))
			}
		}
		seen[name] = true
		placeholder := "value"
		if field.Type() == durationType {
			placeholder = "duration"
		} else {
			switch field.Kind() {
			case reflect.String, reflect.Slice:
				placeholder = "string"
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				placeholder = "int"
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				placeholder = "uint"
			}
		}
		defaultText := value.String()
		showDefault := !field.IsZero()
		if field.CanAddr() && field.Addr().Type().Implements(flagValueType) {
			showDefault = defaultText != ""
		}
		*defs = append(*defs, optionDef{name: name, help: fieldType.Tag.Get("help"), group: fieldType.Tag.Get("group"), value: value, isBool: isBool, placeholder: placeholder, defaultText: defaultText, showDefault: showDefault})
	}
}

func valueFor(v reflect.Value, name string) (flag.Value, bool) {
	if v.CanAddr() && v.Addr().Type().Implements(flagValueType) {
		value := v.Addr().Interface().(flag.Value)
		boolean, ok := value.(interface{ IsBoolFlag() bool })
		return value, ok && boolean.IsBoolFlag()
	}
	if v.Type() == durationType {
		return durationValue{value: v.Addr().Interface().(*time.Duration)}, false
	}
	switch v.Kind() {
	case reflect.String:
		return reflectValue{value: v, kind: "string"}, false
	case reflect.Bool:
		return reflectValue{value: v, kind: "bool"}, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflectValue{value: v, kind: "int"}, false
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflectValue{value: v, kind: "uint"}, false
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			return reflectValue{value: v, kind: "strings"}, false
		}
	}
	panic(fmt.Sprintf("clicmd: unsupported type %s for flag %q", v.Type(), name))
}

type reflectValue struct {
	value reflect.Value
	kind  string
}

func (v reflectValue) IsBoolFlag() bool { return v.kind == "bool" }

func (v reflectValue) String() string {
	if !v.value.IsValid() {
		return ""
	}
	if v.kind == "strings" {
		var values []string
		for i := 0; i < v.value.Len(); i++ {
			values = append(values, v.value.Index(i).String())
		}
		return strings.Join(values, ",")
	}
	return fmt.Sprint(v.value.Interface())
}
func (v reflectValue) Get() any { return v.value.Interface() }
func (v reflectValue) Set(text string) error {
	switch v.kind {
	case "string":
		v.value.SetString(text)
	case "bool":
		value, err := strconv.ParseBool(text)
		if err != nil {
			return err
		}
		v.value.SetBool(value)
	case "int":
		value, err := strconv.ParseInt(text, 0, v.value.Type().Bits())
		if err != nil {
			return err
		}
		v.value.SetInt(value)
	case "uint":
		value, err := strconv.ParseUint(text, 0, v.value.Type().Bits())
		if err != nil {
			return err
		}
		v.value.SetUint(value)
	case "strings":
		v.value.Set(reflect.Append(v.value, reflect.ValueOf(text).Convert(v.value.Type().Elem())))
	}
	return nil
}

type durationValue struct{ value *time.Duration }

func (v durationValue) Set(text string) error {
	parsed, err := time.ParseDuration(text)
	if err == nil {
		*v.value = parsed
	}
	return err
}
func (v durationValue) String() string {
	if v.value == nil {
		return ""
	}
	text := v.value.String()
	if *v.value != 0 && *v.value%time.Minute == 0 {
		return strings.TrimSuffix(text, "0s")
	}
	return text
}
func (v durationValue) Get() any { return *v.value }

func (c *Command) name() string {
	name := ""
	for _, field := range strings.Fields(c.Use) {
		if strings.HasPrefix(field, "[") || strings.ToUpper(field) == field {
			break
		}
		name = field
	}
	return name
}
func (c *Command) child(name string) *Command {
	for _, child := range c.children {
		if child.name() == name {
			return child
		}
	}
	return nil
}
func (c *Command) commandPath() string {
	var parts []string
	for current := c; current != nil; current = current.parent {
		parts = append(parts, current.name())
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, " ")
}

func (c *Command) Usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Usage: %s", strings.TrimSpace(c.Use))
	if len(commandDefs(c)) > 0 || c.InheritRootOptions {
		_, _ = fmt.Fprint(w, " [options]")
	}
	if len(c.children) > 0 {
		_, _ = fmt.Fprint(w, " [command]")
	}
	_, _ = fmt.Fprintln(w)
}

func (c *Command) Help(w io.Writer) {
	c.Usage(w)
	if c.Short != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", c.Short)
	}
	if len(c.children) > 0 {
		children := append([]*Command(nil), c.children...)
		sort.Slice(children, func(i, j int) bool { return children[i].name() < children[j].name() })
		_, _ = fmt.Fprintln(w, "\nCommands:")
		for _, child := range children {
			_, _ = fmt.Fprintf(w, "  %-10s %s\n", child.name(), child.Short)
		}
	}
	for _, group := range c.helpGroups() {
		_, _ = fmt.Fprintf(w, "\n%s:\n", group.name)
		defs := append([]optionDef(nil), group.defs...)
		sort.Slice(defs, func(i, j int) bool { return defs[i].name < defs[j].name })
		for _, def := range defs {
			printOption(w, def)
		}
	}
}

func printOption(w io.Writer, def optionDef) {
	suffix := ""
	if !def.isBool {
		suffix = " " + def.placeholder
	}
	help := def.help
	if def.showDefault {
		value := def.defaultText
		if def.placeholder == "string" && value != "" {
			value = strconv.Quote(value)
		}
		help += " (default " + value + ")"
	}
	if def.isBool && len(def.name) == 1 {
		_, _ = fmt.Fprintf(w, "  -%s\t%s\n", def.name, help)
		return
	}
	_, _ = fmt.Fprintf(w, "  -%s%s\n    \t%s\n", def.name, suffix, help)
}

type helpGroup struct {
	name string
	defs []optionDef
}

func (c *Command) helpGroups() []helpGroup {
	var defs []optionDef
	root := c
	for root.parent != nil {
		root = root.parent
	}
	if c.InheritRootOptions {
		defs = append(defs, commandDefs(root)...)
	}
	defs = append(defs, commandDefs(c)...)
	var result []helpGroup
	indexes := make(map[string]int)
	for _, def := range defs {
		name := strings.TrimSpace(def.group)
		if name == "" {
			name = "Options"
		}
		index, ok := indexes[name]
		if !ok {
			index = len(result)
			indexes[name] = index
			result = append(result, helpGroup{name: name})
		}
		result[index].defs = append(result[index].defs, def)
	}
	return result
}

func NoArgs(_ *Command, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("unexpected argument %q", args[0])
	}
	return nil
}
func ExactArgs(n int) ArgsValidator {
	return func(_ *Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("expected %d argument(s), got %d", n, len(args))
		}
		return nil
	}
}
func MinimumNArgs(n int) ArgsValidator {
	return func(_ *Command, args []string) error {
		if len(args) < n {
			return fmt.Errorf("expected at least %d argument(s), got %d", n, len(args))
		}
		return nil
	}
}
func MaximumArgs(n int) ArgsValidator {
	return func(_ *Command, args []string) error {
		if len(args) > n {
			return fmt.Errorf("expected at most %d argument(s), got %d", n, len(args))
		}
		return nil
	}
}
func RangeArgs(lo, hi int) ArgsValidator {
	return func(c *Command, args []string) error {
		if err := MinimumNArgs(lo)(c, args); err != nil {
			return err
		}
		return MaximumArgs(hi)(c, args)
	}
}
