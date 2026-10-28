// Package syntax is a small, dependency-free syntax highlighter for terminal
// output. Languages are data: comment and string delimiters plus word sets,
// scanned line by line with state carried across lines, which is enough for
// diffs and code snippets where a real parser would be overkill.
package syntax

import (
	"path/filepath"
	"strings"
)

type Kind int

const (
	Plain Kind = iota
	Keyword
	Type
	Builtin
	Constant
	String
	Escape
	Number
	Comment
	Function
	Operator
	Key
	Tag
	Attr
	Heading
	Variable
	Link
)

// Token is a run of text of one kind.
type Token struct {
	Kind Kind
	Text string
}

type Mode int

const (
	ModeCode Mode = iota
	ModeMarkdown
	ModeHTML
	ModeYAML
	ModeTOML
	ModeCSS
	ModeMake
	ModeDocker
)

// StringRule describes one string or character literal syntax.
type StringRule struct {
	Open, Close string
	// Escape means a backslash escapes the next rune.
	Escape bool
	// Multiline strings may continue on the following lines.
	Multiline bool
	// Short literals, like C's 'x', only count as strings when they close
	// within a few runes, so Rust lifetimes and apostrophes stay plain.
	Short bool
}

type Lang struct {
	Name          string
	Mode          Mode
	LineComments  []string
	BlockComments [][2]string
	Strings       []StringRule
	// StringPrefixes are identifiers that may directly precede a string,
	// like Python's r"..." or Rust's b"...".
	StringPrefixes  map[string]bool
	Keywords        map[string]bool
	Types           map[string]bool
	Constants       map[string]bool
	Builtins        map[string]bool
	CaseInsensitive bool
	// CapitalTypes colors capitalized identifiers as types.
	CapitalTypes bool
	// Variables colors shell-style $name, ${name} and $(name).
	Variables bool
	// Annotations colors @name as an attribute (decorators, annotations).
	Annotations bool
	// Preprocessor colors a leading #directive as a keyword.
	Preprocessor bool
	// Macros colors name! as a builtin (Rust macros).
	Macros bool
	// KeyStrings colors a string followed by ':' as a key (JSON).
	KeyStrings bool
	// IdentRunes are extra runes allowed inside identifiers.
	IdentRunes string
}

func words(list string) map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Fields(list) {
		set[word] = true
	}
	return set
}

var (
	doubleQuoted = StringRule{Open: `"`, Close: `"`, Escape: true}
	singleQuoted = StringRule{Open: `'`, Close: `'`, Escape: true}
	charLiteral  = StringRule{Open: `'`, Close: `'`, Escape: true, Short: true}
	cComments    = [][2]string{{"/*", "*/"}}
)

var cFamilyTypes = "bool char double float int long short signed unsigned void size_t int8_t int16_t int32_t int64_t uint8_t uint16_t uint32_t uint64_t"

var languages = map[string]*Lang{
	"go": {
		Name: "go", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:   []StringRule{doubleQuoted, charLiteral, {Open: "`", Close: "`", Multiline: true}},
		Keywords:  words("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var"),
		Types:     words("any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr"),
		Constants: words("true false nil iota"),
		Builtins:  words("append cap clear close complex copy delete imag len make max min new panic print println real recover"),
	},
	"python": {
		Name: "python", LineComments: []string{"#"},
		Strings: []StringRule{
			{Open: `"""`, Close: `"""`, Escape: true, Multiline: true},
			{Open: `'''`, Close: `'''`, Escape: true, Multiline: true},
			doubleQuoted, singleQuoted,
		},
		StringPrefixes: words("r b f u rb br fr rf R B F U"),
		Keywords:       words("and as assert async await break class continue def del elif else except finally for from global if import in is lambda match case nonlocal not or pass raise return try while with yield"),
		Types:          words("int float str bytes bool list dict set tuple object complex frozenset bytearray type"),
		Constants:      words("True False None self cls"),
		Builtins:       words("print len range open isinstance issubclass enumerate zip map filter sorted reversed sum min max abs any all iter next super getattr setattr hasattr repr format input"),
		Annotations:    true,
	},
	"javascript": {
		Name: "javascript", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{doubleQuoted, singleQuoted, {Open: "`", Close: "`", Escape: true, Multiline: true}},
		Keywords:     words("async await break case catch class const continue debugger default delete do else export extends finally for from function if import in instanceof let new of return static super switch this throw try typeof var void while with yield get set"),
		Constants:    words("true false null undefined NaN Infinity"),
		Builtins:     words("console window document globalThis require module exports process JSON Math Object Array String Number Boolean Promise Map Set Symbol Error Date RegExp"),
		CapitalTypes: true, IdentRunes: "$", Annotations: true,
	},
	"typescript": {
		Name: "typescript", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{doubleQuoted, singleQuoted, {Open: "`", Close: "`", Escape: true, Multiline: true}},
		Keywords:     words("abstract as async await break case catch class const continue debugger declare default delete do else enum export extends finally for from function if implements import in infer instanceof interface is keyof let namespace new of private protected public readonly return satisfies static super switch this throw try type typeof var void while with yield get set"),
		Types:        words("any boolean never number object string symbol unknown void bigint"),
		Constants:    words("true false null undefined NaN Infinity"),
		Builtins:     words("console window document globalThis require module exports process JSON Math Object Array String Number Boolean Promise Map Set Symbol Error Date RegExp Record Partial Readonly Pick Omit"),
		CapitalTypes: true, IdentRunes: "$", Annotations: true,
	},
	"rust": {
		Name: "rust", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:        []StringRule{doubleQuoted, charLiteral},
		StringPrefixes: words("b r br"),
		Keywords:       words("as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while"),
		Types:          words("bool char f32 f64 i8 i16 i32 i64 i128 isize str u8 u16 u32 u64 u128 usize String Vec Option Result Box Rc Arc HashMap HashSet"),
		Constants:      words("true false None Some Ok Err"),
		CapitalTypes:   true, Macros: true,
	},
	"c": {
		Name: "c", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{doubleQuoted, charLiteral},
		Keywords:     words("auto break case const continue default do else enum extern for goto if inline register restrict return sizeof static struct switch typedef union volatile while"),
		Types:        words(cFamilyTypes + " FILE"),
		Constants:    words("NULL true false"),
		Builtins:     words("printf fprintf sprintf snprintf malloc calloc realloc free memcpy memset strlen strcmp"),
		Preprocessor: true,
	},
	"cpp": {
		Name: "cpp", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{doubleQuoted, charLiteral},
		Keywords:     words("alignas alignof auto break case catch class const constexpr const_cast continue decltype default delete do dynamic_cast else enum explicit export extern for friend goto if inline mutable namespace new noexcept operator private protected public register reinterpret_cast return sizeof static static_assert static_cast struct switch template this throw try typedef typeid typename union using virtual volatile while override final"),
		Types:        words(cFamilyTypes + " wchar_t char8_t char16_t char32_t"),
		Constants:    words("true false nullptr NULL"),
		Builtins:     words("std cout cerr endl string vector map unordered_map shared_ptr unique_ptr make_shared make_unique move"),
		Preprocessor: true, CapitalTypes: true,
	},
	"java": {
		Name: "java", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{{Open: `"""`, Close: `"""`, Escape: true, Multiline: true}, doubleQuoted, charLiteral},
		Keywords:     words("abstract assert break case catch class continue default do else enum extends final finally for if implements import instanceof interface native new package private protected public record return sealed static super switch synchronized this throw throws transient try var void volatile while yield"),
		Types:        words("boolean byte char double float int long short String Object Integer Long Double Boolean List Map Set"),
		Constants:    words("true false null"),
		CapitalTypes: true, Annotations: true,
	},
	"csharp": {
		Name: "csharp", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:        []StringRule{doubleQuoted, charLiteral},
		StringPrefixes: words("@ $"),
		Keywords:       words("abstract as async await base break case catch checked class const continue default delegate do else enum event explicit extern finally fixed for foreach goto if implicit in interface internal is lock namespace new operator out override params private protected public readonly record ref return sealed sizeof stackalloc static struct switch this throw try typeof unchecked unsafe using var virtual void volatile while yield get set init"),
		Types:          words("bool byte char decimal double float int long object sbyte short string uint ulong ushort dynamic"),
		Constants:      words("true false null"),
		CapitalTypes:   true, Preprocessor: true,
	},
	"kotlin": {
		Name: "kotlin", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{{Open: `"""`, Close: `"""`, Multiline: true}, doubleQuoted, charLiteral},
		Keywords:     words("abstract as break by catch class companion const constructor continue data do else enum external final finally for fun get if import in init inline interface internal is lateinit object open operator out override package private protected public return sealed set super suspend this throw try typealias val var vararg when where while"),
		Constants:    words("true false null"),
		CapitalTypes: true, Annotations: true,
	},
	"swift": {
		Name: "swift", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:      []StringRule{{Open: `"""`, Close: `"""`, Escape: true, Multiline: true}, doubleQuoted},
		Keywords:     words("actor as associatedtype async await break case catch class continue default defer deinit do else enum extension fallthrough fileprivate for func guard if import in init inout internal is let mutating nonisolated open operator private protocol public repeat rethrows return self Self some static struct subscript super switch throw throws try typealias var where while"),
		Constants:    words("true false nil"),
		CapitalTypes: true, Annotations: true,
	},
	"shell": {
		Name: "shell", LineComments: []string{"#"},
		Strings:   []StringRule{doubleQuoted, {Open: `'`, Close: `'`}},
		Keywords:  words("if then else elif fi case esac for select while until do done in function time return local export readonly declare unset shift break continue"),
		Builtins:  words("echo printf cd pwd exit eval exec source set test read trap wait kill alias type command true false mkdir rm cp mv cat grep sed awk find xargs"),
		Variables: true, IdentRunes: "-",
	},
	"makefile": {
		Name: "makefile", Mode: ModeMake, LineComments: []string{"#"},
		Strings:   []StringRule{doubleQuoted, {Open: `'`, Close: `'`}},
		Keywords:  words("ifeq ifneq ifdef ifndef else endif include override export unexport define endef vpath"),
		Builtins:  words(".PHONY .DEFAULT .SUFFIXES .SILENT"),
		Variables: true,
	},
	"dockerfile": {
		Name: "dockerfile", Mode: ModeDocker, LineComments: []string{"#"},
		Strings:   []StringRule{doubleQuoted, {Open: `'`, Close: `'`}},
		Keywords:  words("FROM AS RUN CMD LABEL MAINTAINER EXPOSE ENV ADD COPY ENTRYPOINT VOLUME USER WORKDIR ARG ONBUILD STOPSIGNAL HEALTHCHECK SHELL"),
		Variables: true,
	},
	"json": {
		Name: "json", LineComments: []string{"//"}, BlockComments: cComments,
		Strings:    []StringRule{doubleQuoted},
		Constants:  words("true false null"),
		KeyStrings: true,
	},
	"yaml": {
		Name: "yaml", Mode: ModeYAML, LineComments: []string{"#"},
		Strings:   []StringRule{doubleQuoted, {Open: `'`, Close: `'`}},
		Constants: words("true false null yes no on off True False Null Yes No On Off TRUE FALSE NULL ~"),
	},
	"toml": {
		Name: "toml", Mode: ModeTOML, LineComments: []string{"#"},
		Strings: []StringRule{
			{Open: `"""`, Close: `"""`, Escape: true, Multiline: true},
			{Open: `'''`, Close: `'''`, Multiline: true},
			doubleQuoted, {Open: `'`, Close: `'`},
		},
		Constants: words("true false inf nan"),
	},
	"markdown": {Name: "markdown", Mode: ModeMarkdown},
	"html": {
		Name: "html", Mode: ModeHTML, BlockComments: [][2]string{{"<!--", "-->"}},
		Strings: []StringRule{doubleQuoted, {Open: `'`, Close: `'`}},
	},
	"css": {
		Name: "css", Mode: ModeCSS, LineComments: nil, BlockComments: cComments,
		Strings:    []StringRule{doubleQuoted, singleQuoted},
		Constants:  words("inherit initial unset none auto important"),
		IdentRunes: "-",
	},
	"sql": {
		Name: "sql", LineComments: []string{"--"}, BlockComments: cComments,
		Strings:         []StringRule{{Open: `'`, Close: `'`}, {Open: `"`, Close: `"`}},
		CaseInsensitive: true,
		Keywords:        words("add all alter and any as asc begin between by case check column commit constraint create cross database default delete desc distinct drop else end exists foreign from full group having if in index inner insert into is join key left like limit not null offset on or order outer primary references returning right rollback select set table then transaction truncate union unique update using values view when where with"),
		Types:           words("int integer bigint smallint serial bigserial decimal numeric real float double boolean bool char varchar text date time timestamp timestamptz interval uuid json jsonb blob bytea"),
		Constants:       words("true false null"),
		Builtins:        words("count sum avg min max coalesce now lower upper length substring cast"),
	},
}

func init() {
	// Languages that share another's spec.
	languages["scss"] = languages["css"]
	languages["xml"] = languages["html"]
	// Lowercase lookups for case-insensitive languages.
	for _, lang := range languages {
		if lang.CaseInsensitive {
			for _, set := range []map[string]bool{lang.Keywords, lang.Types, lang.Constants, lang.Builtins} {
				for word := range set {
					set[strings.ToLower(word)] = true
				}
			}
		}
	}
}

var aliases = map[string]string{
	"golang": "go", "py": "python", "python3": "python",
	"js": "javascript", "jsx": "javascript", "mjs": "javascript", "cjs": "javascript", "node": "javascript",
	"ts": "typescript", "tsx": "typescript", "mts": "typescript", "cts": "typescript",
	"rs": "rust", "h": "c", "c++": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp", "hh": "cpp", "hxx": "cpp",
	"cs": "csharp", "c#": "csharp", "kt": "kotlin", "kts": "kotlin",
	"sh": "shell", "bash": "shell", "zsh": "shell", "ksh": "shell", "console": "shell", "shellscript": "shell",
	"make": "makefile", "mk": "makefile", "mak": "makefile", "docker": "dockerfile", "containerfile": "dockerfile",
	"jsonc": "json", "json5": "json", "jsonl": "json", "yml": "yaml",
	"md": "markdown", "mdx": "markdown", "htm": "html", "xhtml": "html", "svg": "xml", "vue": "html",
}

// Lookup finds a language by name or alias, as in a markdown fence's info
// string. It returns nil for unknown names.
func Lookup(name string) *Lang {
	name = strings.ToLower(strings.TrimSpace(name))
	if name, _, _ = strings.Cut(name, " "); name == "" {
		return nil
	}
	name = strings.TrimPrefix(name, "{.")
	name = strings.TrimSuffix(name, "}")
	if lang, ok := languages[name]; ok {
		return lang
	}
	if alias, ok := aliases[name]; ok {
		return languages[alias]
	}
	return nil
}

var shebangs = map[string]string{
	"sh": "shell", "bash": "shell", "zsh": "shell", "ksh": "shell", "dash": "shell",
	"python": "python", "python3": "python", "node": "javascript", "deno": "typescript",
}

// Detect picks the language of a file from its name, falling back to a
// shebang on its first line. It returns nil when the language is unknown.
func Detect(path, firstLine string) *Lang {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case base == "makefile" || base == "gnumakefile":
		return languages["makefile"]
	case base == "dockerfile" || base == "containerfile" || strings.HasPrefix(base, "dockerfile.") || strings.HasSuffix(base, ".dockerfile"):
		return languages["dockerfile"]
	case base == ".bashrc" || base == ".zshrc" || base == ".profile" || base == ".bash_profile" || base == ".envrc":
		return languages["shell"]
	}
	if ext := strings.TrimPrefix(filepath.Ext(base), "."); ext != "" {
		if lang := Lookup(ext); lang != nil {
			return lang
		}
	}
	if interpreter, ok := strings.CutPrefix(firstLine, "#!"); ok {
		fields := strings.Fields(interpreter)
		if len(fields) > 0 {
			name := filepath.Base(fields[0])
			if name == "env" && len(fields) > 1 {
				name = fields[len(fields)-1]
			}
			if lang, ok := shebangs[name]; ok {
				return languages[lang]
			}
		}
	}
	return nil
}
