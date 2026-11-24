package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// dataLiterals are Chinese string literals that are data in the files
// named, not user-visible text: values compared or stored, never shown as
// such. Everything else with a CJK character must have an English entry.
var dataLiterals = map[string]string{
	// model.CoreDownload.Phase values, which env.CoreDownloadText switches
	// on and words itself.
	"下载": "coredl.go env.go", "校验": "coredl.go env.go", "安装": "coredl.go env.go", "启动": "coredl.go env.go",
	// A default site name: user data.
	"百度": "settings.go",
	// Parts of the core's exit message (internal/core/runner.go) that
	// errlog.go looks for.
	"最近日志：": "errlog.go", "退出码 0": "errlog.go", "，, \n": "errlog.go",
}

// hasCJK reports whether s holds a Han character or CJK/fullwidth
// punctuation.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

// keyTables are the declarations, by file, whose Chinese literals are keys
// that reach T where they are read rather than where they are written: a
// table's column titles (env.I18nColumns), the options of a control
// (Options), names looked up with T(table[i]). A name ending in "()" is a
// function whose literals are passed on to a helper that translates them.
// A literal anywhere else must be an argument of T, Tf, Tn, Error, Options
// or KeyOf (TestChineseGoesThroughT).
var keyTables = map[string]string{
	"internal/view/capture/page.go":         "columns",
	"internal/view/conns/page.go":           "allColumns",
	"internal/view/conns/topology_graph.go": "columnTitles",
	"internal/view/logs/page.go":            "levelNames",
	"internal/view/netpage/page.go":         "siteCols",
	"internal/view/probe/page.go":           "regionCols unlockCols",
	"internal/view/rules/page.go":           "columns",
	"internal/view/stats/page.go":           "columns dimLabels rangeLabels",
	"internal/view/subs/dialogs.go":         "autoLabels",
	"internal/view/subs/page.go":            "columns",
	"internal/view/settings/page.go":        "tabNames",
	"internal/view/settings/cores.go":       "channelNames",
	"internal/view/settings/mirror.go":      "mirrorDirect mirrorCustom",
	"internal/view/settings/wifi.go":        "wifiNoSwitch",
	// parse hands the messages to its fill, which formats them with Tf.
	"internal/view/settings/atrust.go": "parse()",
	// ATrustReferenceYAML returns T(atrustReference).
	"internal/app/atrustcfg.go": "atrustReference",
	// The checks' names, which the 检测 page shows through T.
	"internal/app/unlock.go": "unlockChecks",
}

// pendingUnwrapped are Chinese literals known to be shown without going
// through T, so that an English window shows Chinese: each has an English
// entry that nothing reads. They wait for their owners to fix them (wrap
// the text where it is shown, or make the sentinel an i18n.Error); the
// list only keeps TestChineseGoesThroughT green until then. Do not add
// to it: new text goes through T.
var pendingUnwrapped = map[string]string{
	// errors.New / fmt.Errorf with a Chinese text, shown by the launcher.
	"与澜流后台服务的连接已断开，正在重新连接": "internal/app/remote/client.go",
	// fmt.Sprintf with the Chinese text, written into the exported YAML.
	"(%d 项，已隐藏)": "internal/app/capture.go",
	// The separator of a list inside a translated sentence.
	"、": "internal/app/leak.go",
}

// literal is a Chinese string literal of the sources.
type literal struct {
	text  string
	file  string // relative to the repository root, with slashes
	line  int
	decl  string // the top-level declaration it is in: "columns", "parse()"
	viaT  bool   // an argument of T, Tf, Tn, Error, Options, KeyOf or env.I18nColumns
	count bool   // the key of a Tn
}

func (l literal) at() string { return l.file + ":" + strconv.Itoa(l.line) }

// sourceLiterals collects every Chinese string literal of the user-facing
// packages: internal/view/**, internal/app (not the fake backend, whose
// Chinese is sample data), internal/syncfile (lanflow.yaml's errors), internal/split, the
// main package, and the path notes of
// internal/locate/analyze.go and direct.go (the rest of locate holds ISP
// names: data). The dataLiterals are left out.
func sourceLiterals(t *testing.T) []literal {
	t.Helper()
	root := filepath.Join("..", "..")
	var files []string
	for _, dir := range []string{"internal/view", "internal/app", "internal/syncfile", "internal/split"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "fake" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"main.go", "main_service.go", filepath.Join("internal", "locate", "analyze.go"), filepath.Join("internal", "locate", "direct.go")} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			files = append(files, filepath.Join(root, f))
		}
	}
	var out []literal
	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		viaT, counts := translated(file)
		collect := func(decl string, n ast.Node) {
			ast.Inspect(n, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !hasCJK(s) || strings.Contains(dataLiterals[s], filepath.Base(path)) {
					return true
				}
				out = append(out, literal{text: s, file: rel, line: fset.Position(lit.Pos()).Line, decl: decl, viaT: viaT[lit], count: counts[lit]})
				return true
			})
		}
		for _, d := range file.Decls {
			switch x := d.(type) {
			case *ast.FuncDecl:
				collect(x.Name.Name+"()", x)
			case *ast.GenDecl:
				for _, sp := range x.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
						collect(vs.Names[0].Name, vs)
					} else {
						collect("", sp)
					}
				}
			}
		}
	}
	return out
}

// translated finds the string literals of file that are handed to the
// catalog where they are written: the key of i18n.T, Tf, Error and Has,
// the key of Tn (its second argument; counts tells those), and every
// literal among the arguments of Options, KeyOf and env.I18nColumns.
func translated(file *ast.File) (viaT, counts map[*ast.BasicLit]bool) {
	viaT, counts = map[*ast.BasicLit]bool{}, map[*ast.BasicLit]bool{}
	key := func(args []ast.Expr, i int) *ast.BasicLit {
		if i < len(args) {
			if lit, ok := args[i].(*ast.BasicLit); ok {
				return lit
			}
		}
		return nil
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch pkg.Name + "." + sel.Sel.Name {
		case "i18n.T", "i18n.Tf", "i18n.Error", "i18n.Has":
			if lit := key(call.Args, 0); lit != nil {
				viaT[lit] = true
			}
		case "i18n.Tn":
			if lit := key(call.Args, 1); lit != nil {
				viaT[lit], counts[lit] = true, true
			}
		case "i18n.Options", "i18n.KeyOf", "env.I18nColumns":
			for _, a := range call.Args {
				ast.Inspect(a, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok {
						viaT[lit] = true
					}
					return true
				})
			}
		}
		return true
	})
	return viaT, counts
}

// TestEveryKeyHasEnglish fails when a Chinese string of the sources has no
// English entry, and when an entry translates nothing in the sources. An
// entry for a count of one ("…|one", Tn) translates the key of a Tn call.
func TestEveryKeyHasEnglish(t *testing.T) {
	lits := sourceLiterals(t)
	keys, counted := map[string][]string{}, map[string]bool{}
	for _, l := range lits {
		keys[l.text] = append(keys[l.text], l.at())
		counted[l.text] = counted[l.text] || l.count
	}
	if len(keys) < 500 {
		t.Fatalf("found only %d Chinese strings; is the scan looking at the right tree?", len(keys))
	}
	var missing []string
	for k, where := range keys {
		if !Has(k) {
			missing = append(missing, strconv.Quote(k)+"  // "+where[0])
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("no English entry for %s", m)
	}
	var unused []string
	for _, k := range Keys() {
		if base, one := strings.CutSuffix(k, PluralOne); one {
			switch {
			case !Has(base):
				t.Errorf("English entry %q has no entry for the other counts (%q)", k, base)
			case keys[base] == nil:
				unused = append(unused, strconv.Quote(k))
			case !counted[base]:
				t.Errorf("English entry %q is never read: %q is not the key of a Tn call (%s)", k, base, keys[base][0])
			}
			continue
		}
		if _, ok := keys[k]; !ok {
			unused = append(unused, strconv.Quote(k))
		}
	}
	sort.Strings(unused)
	for _, u := range unused {
		t.Errorf("English entry %s is used nowhere", u)
	}
}

// TestChineseGoesThroughT fails when a Chinese literal of the sources is
// neither handed to the catalog where it is written (T, Tf, Tn, Error,
// Options, KeyOf, env.I18nColumns) nor part of a registered key table
// (keyTables): such a text has an English entry, since another place uses
// the same words, and still shows in Chinese in an English window.
func TestChineseGoesThroughT(t *testing.T) {
	inTable := func(l literal) bool {
		for _, name := range strings.Fields(keyTables[l.file]) {
			if name == l.decl {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, l := range sourceLiterals(t) {
		if l.viaT || inTable(l) {
			continue
		}
		if file, ok := pendingUnwrapped[l.text]; ok && file == l.file {
			seen[l.text] = true
			continue
		}
		t.Errorf("%s: %q is shown without i18n.T (in %s): pass it to T/Tf/Tn/Error, or register its table in keyTables", l.at(), l.text, l.decl)
	}
	for text, file := range pendingUnwrapped {
		if !seen[text] {
			// Fixed by its owner meanwhile: only a reminder to drop it.
			t.Logf("pendingUnwrapped: %q is no longer an unwrapped literal of %s; remove it from the list", text, file)
		}
	}
	// The tables registered exist: a renamed one would pass unnoticed.
	have := map[string]bool{}
	for _, l := range sourceLiterals(t) {
		have[l.file+" "+l.decl] = true
	}
	for file, names := range keyTables {
		for _, name := range strings.Fields(names) {
			if !have[file+" "+name] {
				t.Logf("keyTables: %s has no Chinese literal in %s any more", file, name)
			}
		}
	}
}

// TestEnglishHasNoCJK keeps the English table English.
func TestEnglishHasNoCJK(t *testing.T) {
	for k, v := range en {
		if hasCJK(v) {
			t.Errorf("English of %q contains CJK text: %q", k, v)
		}
		if strings.TrimSpace(v) == "" {
			t.Errorf("English of %q is empty", k)
		}
	}
}

// TestFormatVerbsMatch checks that a translation reads the arguments its
// key reads, each with the same verb, so Tf and fmt.Errorf get what they
// expect. The order may differ where English needs it ("%[2]s of %[1]d"),
// and flags, widths and precisions are the translation's own ("%02d").
func TestFormatVerbsMatch(t *testing.T) {
	for k, v := range en {
		if kv, vv := verbs(k), verbs(v); kv != vv {
			t.Errorf("format verbs differ for %q: key reads %q, English reads %q", k, kv, vv)
		}
	}
}

// verbs lists the arguments a format reads with the verb that reads each,
// as "1:d 2:s", sorted and without repeats: fmt's syntax, with flags,
// width, precision and explicit argument indexes ("%[3]*.[2]*[1]f").
func verbs(s string) string {
	set := map[string]bool{}
	next := 1
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		i++
		if i < len(s) && s[i] == '%' {
			continue
		}
		arg := 0 // the explicit index before what comes next, if any
		index := func() {
			if i < len(s) && s[i] == '[' {
				if j := strings.IndexByte(s[i:], ']'); j > 0 {
					if n, err := strconv.Atoi(s[i+1 : i+j]); err == nil && n > 0 {
						arg = n
					}
					i += j + 1
				}
			}
		}
		take := func(verb byte) {
			n := next
			if arg > 0 {
				n = arg
			}
			set[strconv.Itoa(n)+":"+string(verb)] = true
			next, arg = n+1, 0
		}
		number := func() { // a width or a precision: digits, or * from an argument
			index()
			if i < len(s) && s[i] == '*' {
				take('*')
				i++
				return
			}
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
		}
		for i < len(s) && strings.IndexByte("+-# 0", s[i]) >= 0 {
			i++
		}
		number()
		if i < len(s) && s[i] == '.' {
			i++
			number()
		}
		index()
		if i >= len(s) {
			break
		}
		take(s[i])
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// TestVerbs: the parsing TestFormatVerbsMatch rests on.
func TestVerbs(t *testing.T) {
	for _, c := range []struct{ format, want string }{
		{"", ""},
		{"100%% 完成", ""},
		{"%d 个连接", "1:d"},
		{"%s · %d 个匹配", "1:s 2:d"},
		{"%02d:%02d", "1:d 2:d"},
		{"%.1f GB", "1:f"},
		{"%-8s|%+d|%#x|% d", "1:s 2:d 3:x 4:d"},
		{"%[2]s of %[1]d", "1:d 2:s"},
		{"%[1]d and again %[1]d", "1:d"},
		{"%[2]d then %s", "2:d 3:s"},
		{"%*d", "1:* 2:d"},
		{"%[3]*.[2]*[1]f", "1:f 2:* 3:*"}, // fmt's own example: %6.2f of the first
		{"%[2]*.[1]d", "1:d 2:*"},
		{"%6.2f%%", "1:f"},
		{"%v: %w", "1:v 2:w"},
		{"trailing %", ""},
	} {
		if got := verbs(c.format); got != c.want {
			t.Errorf("verbs(%q) = %q, want %q", c.format, got, c.want)
		}
	}
	// What the old one-byte reading got wrong.
	if verbs("%d 分 %s") != verbs("%[2]s, %[1]d min") {
		t.Error("a translation may reorder its arguments")
	}
	if verbs("%d") != verbs("%02d") {
		t.Error("a translation may pad a number")
	}
	if verbs("%d %s") == verbs("%s %d") {
		t.Error("swapped verbs without indexes read the wrong arguments")
	}
	if verbs("%d %s") == verbs("%[1]d") {
		t.Error("a translation that drops an argument must differ")
	}
}

// TestPlural: Tn says the entry for one where English has it, and the
// entry of the key for every other count and in Chinese.
func TestPlural(t *testing.T) {
	t.Cleanup(func() { SetLang(ZH) })
	const key = "%d 个活动连接"
	SetLang(EN)
	for n, want := range map[int]string{0: "0 active connections", 1: "1 active connection", 2: "2 active connections", 21: "21 active connections"} {
		if got := Tn(n, key, n); got != want {
			t.Errorf("Tn(%d) = %q, want %q", n, got, want)
		}
	}
	// A key without an entry for one says its only form.
	if got := Tn(1, "%d 分钟前", 1); got != "1 min ago" {
		t.Errorf("no entry for one: %q", got)
	}
	SetLang(ZH)
	if got := Tn(1, key, 1); got != "1 个活动连接" {
		t.Errorf("Chinese: %q", got)
	}
	if T(key+PluralOne) != key {
		t.Errorf("the context of a key must not show: %q", T(key+PluralOne))
	}
	// Every entry for one differs from the entry for the other counts,
	// and no English text settles for "(s)".
	for k, v := range en {
		if base, one := strings.CutSuffix(k, PluralOne); one && v == en[base] {
			t.Errorf("%q says the same as %q: %q", k, base, v)
		}
	}
}

func TestLangAndNormalize(t *testing.T) {
	t.Cleanup(func() { SetLang(ZH) })
	for in, want := range map[string]string{"": ZH, "zh": ZH, "zh-Hans-CN": ZH, "en": EN, "en-GB": EN, "English": EN, "fr": ZH} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		pref []string
		want string
	}{
		{nil, ZH}, {[]string{"zh-Hans-CN", "en"}, ZH}, {[]string{"zh-TW"}, ZH}, {[]string{"en-US", "zh"}, EN}, {[]string{"de-DE"}, EN},
	} {
		if got := FromSystem(c.pref); got != c.want {
			t.Errorf("FromSystem(%q) = %q, want %q", c.pref, got, c.want)
		}
	}
	if Tag(ZH) != "zh-Hans" || Tag(EN) != "en" || Autonym(ZH) != "简体中文" || Autonym(EN) != "English" {
		t.Error("Tag or Autonym wrong")
	}
	SetLang(EN)
	if T("取消") != "Cancel" || Tf("%d 分钟前", 5) != "5 min ago" || Error("取消").Error() != "Cancel" {
		t.Errorf("English lookup: %q %q", T("取消"), Tf("%d 分钟前", 5))
	}
	if T("not a key") != "not a key" {
		t.Error("unknown keys must come back unchanged")
	}
	if T("规则|页") != "Rules" || T("规则") != "Rule" {
		t.Errorf("context keys: %q %q", T("规则|页"), T("规则"))
	}
	if KeyOf("Cancel", []string{"取消", "好"}) != "取消" || Options("取消", "好")[1] != "OK" {
		t.Error("Options/KeyOf")
	}
	SetLang(ZH)
	if T("取消") != "取消" || Error("取消").Error() != "取消" {
		t.Error("Chinese must return the key")
	}
	if T("规则|页") != "规则" || T("没有的键|上下文") != "没有的键" {
		t.Error("Chinese must drop the context of a key")
	}
}
