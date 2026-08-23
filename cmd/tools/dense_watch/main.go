package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/golangast/dense/internal/ai/dense"
	"github.com/golangast/dense/internal/generator"
)

var globalAutoApply bool
var globalAutoRestoreGit bool

// previewAutoFix returns formatted file bytes if an AutoFix would apply (without writing).
func previewAutoFix(d generator.GoError) ([]byte, error) {
	if d.FilePath == "" {
		return nil, fmt.Errorf("empty file")
	}
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, d.FilePath, nil, parser.ParseComments)
	if perr != nil {
		// If parse failed due to a common top-level syntax error, attempt a
		// conservative raw edit preview (commenting the offending line) so the
		// user can see the suggested auto-fix without us writing to disk.
		if strings.Contains(d.Message, "non-declaration statement outside function body") {
			data, rerr := os.ReadFile(d.FilePath)
			if rerr != nil {
				return nil, fmt.Errorf("parse: %w", perr)
			}
			lines := strings.Split(string(data), "\n")
			ln := d.Line - 1
			if ln >= 0 && ln < len(lines) {
				if !strings.HasPrefix(strings.TrimSpace(lines[ln]), "//") {
					lines[ln] = "// " + lines[ln]
				}
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		return nil, fmt.Errorf("parse: %w", perr)
	}
	fixed := false
	for _, match := range regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(d.Message, -1) {
		if len(match) < 2 {
			continue
		}
		ident := match[1]
		if pkgPath, exists := generator.StdPkgMap()[ident]; exists {
			// add import
			newImport := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", pkgPath)}}
			inserted := false
			for _, decl := range file.Decls {
				if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
					gd.Specs = append(gd.Specs, newImport)
					inserted = true
					break
				}
			}
			if !inserted {
				file.Decls = append([]ast.Decl{&ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{newImport}}}, file.Decls...)
			}
			fixed = true
		}
	}
	if !fixed {
		return nil, fmt.Errorf("no auto-fix applicable")
	}
	var sb strings.Builder
	if err := format.Node(&sb, fset, file); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// previewAddContext returns formatted bytes after injecting context into a copy of the file.
func previewAddContext(filePath, symbol, dir string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if !dense.EnsureContextParamInFile(file, symbol) {
		return nil, fmt.Errorf("could not inject context into %s", symbol)
	}
	var sb strings.Builder
	if err := format.Node(&sb, fset, file); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

// showDiff prints a simple unified-style diff between the on-disk file and newContent.
func showDiff(path string, newContent []byte) {
	old, _ := os.ReadFile(path)
	if string(old) == string(newContent) {
		fmt.Printf("(no change for %s)\n", path)
		return
	}
	fmt.Printf("--- %s (orig)\n+++ %s (new)\n", path, path)
	// naive line-based diff
	oldLines := strings.Split(string(old), "\n")
	newLines := strings.Split(string(newContent), "\n")
	max := len(oldLines)
	if len(newLines) > max {
		max = len(newLines)
	}
	for i := 0; i < max; i++ {
		var o, n string
		if i < len(oldLines) {
			o = oldLines[i]
		}
		if i < len(newLines) {
			n = newLines[i]
		}
		if o != n {
			if o != "" {
				fmt.Printf("- %s\n", o)
			}
			if n != "" {
				fmt.Printf("+ %s\n", n)
			}
		}
	}
}

func main() {
	dir := flag.String("dir", ".", "workspace directory to watch")
	debounce := flag.Duration("debounce", 500*time.Millisecond, "debounce interval for file events")
	autoApply := flag.Bool("auto-apply", false, "automatically apply previewed fixes without prompting")
	autoRestoreGit := flag.Bool("auto-restore-git", false, "on verification failure, restore files from git HEAD when available")
	poll := flag.Duration("poll", 0, "periodically run suggestion pass (no save required); 0 disables")
	flag.Parse()
	// set globals used by runSuggestions
	globalAutoApply = *autoApply
	globalAutoRestoreGit = *autoRestoreGit

	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("invalid dir: %v", err)
	}
	log.Printf("watching %s", abs)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("watcher error: %v", err)
	}
	defer w.Close()

	done := make(chan struct{})
	events := make(chan struct{}, 1)

	go func() {
		for {
			select {
			case ev := <-w.Events:
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					select {
					case events <- struct{}{}:
					default:
					}
				}
			case err := <-w.Errors:
				log.Printf("watch error: %v", err)
			}
		}
	}()

	// If polling is enabled, start a ticker that triggers suggestion passes
	if *poll > 0 {
		go func() {
			t := time.NewTicker(*poll)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					select {
					case events <- struct{}{}:
					default:
					}
				}
			}
		}()
	}

	// walk and watch directories
	filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// skip hidden dirs like .git
			if len(d.Name()) > 0 && d.Name()[0] == '.' {
				return filepath.SkipDir
			}
			w.Add(path)
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		// debounce loop
		var timer *time.Timer
		var ch <-chan time.Time
		for {
			select {
			case <-events:
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(*debounce)
				ch = timer.C
			case <-ctx.Done():
				return
			case <-ch:
				// run suggestion pass
				runSuggestions(abs)
				// reset channel so we don't re-enter until next event
				ch = nil
			}
		}
	}()

	<-done
}

func runSuggestions(dir string) {
	fmt.Println("=== dense: running suggestion pass ===")
	// 1. diagnose project errors
	diags, derr := generator.DiagnoseProject(dir)
	if derr != nil {
		fmt.Printf("diagnose error: %v\n", derr)
	}
	options := []string{}
	// diagnostics -> offer auto-fix options
	if len(diags) == 0 {
		fmt.Println("No compiler diagnostics found.")
	} else {
		fmt.Printf("Found %d diagnostics:\n", len(diags))
		for i, d := range diags {
			fmt.Printf("- %s:%d:%d %s\n", d.FilePath, d.Line, d.Column, d.Message)
			options = append(options, fmt.Sprintf("Auto-fix diagnostic %d: %s", i+1, d.Message))
		}
	}

	// 2. index workspace and offer proactive suggestions
	wgraph, err := dense.IndexWorkspace(dir)
	if err != nil {
		fmt.Printf("workspace index error: %v\n", err)
		return
	}

	// simple heuristics: suggest adding context param to funcs with 'context' in name
	type opt struct {
		kind   string // "diag" or "add_context"
		index  int    // diag index
		file   string
		symbol string
	}
	opts := []opt{}
	for fp, f := range wgraph.Files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name := fn.Name.Name
				lname := strings.ToLower(name)
				if (strings.Contains(lname, "handler") || strings.Contains(lname, "serve") || strings.Contains(lname, "start")) && !hasContextParam(fn) {
					desc := fmt.Sprintf("Inject context.Context into %s (file: %s)", name, fp)
					fmt.Println("- ", desc)
					opts = append(opts, opt{kind: "add_context", file: fp, symbol: name})
					options = append(options, desc)
				}
			}
		}
	}

	// If we have actionable options, prompt the user to choose one.
	if len(options) == 0 {
		fmt.Println("=== dense: suggestion pass complete ===")
		return
	}

	fmt.Println("\nChoose an option to apply (comma-separated numbers), or press Enter to skip:")
	for i, o := range options {
		fmt.Printf("%d) %s\n", i+1, o)
	}
	// If auto-apply is enabled, automatically choose all diagnostics.
	reader := bufio.NewReader(os.Stdin)
	var picks []string
	line := ""
	if globalAutoApply && len(diags) > 0 {
		// choose diagnostics only (they are the first options)
		for i := 1; i <= len(diags); i++ {
			picks = append(picks, strconv.Itoa(i))
		}
		fmt.Printf("Auto-apply enabled: selecting diagnostics %s\n", strings.Join(picks, ","))
	} else {
		fmt.Print("Select (prefix with 'p' to preview, e.g. p1,2): ")
		line, _ = reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Println("No selection; skipping.")
			fmt.Println("=== dense: suggestion pass complete ===")
			return
		}
		picks = strings.Split(line, ",")
	}

	// if user requested preview (input starts with 'p')
	previewMode := false
	if strings.HasPrefix(strings.ToLower(line), "p") {
		previewMode = true
		// strip leading p from each pick token
		for i := range picks {
			picks[i] = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(picks[i]), "p"))
		}
	}

	// transactionally apply all chosen fixes with backups and a verification step
	backups := map[string][]byte{}
	modified := map[string]bool{}
	appliedAny := false
	for _, p := range picks {
		p = strings.TrimSpace(p)
		idx, err := strconv.Atoi(p)
		if err != nil || idx < 1 || idx > len(options) {
			fmt.Printf("invalid choice: %s\n", p)
			continue
		}
		chosen := idx - 1
		// preview mode: show diffs for each chosen and continue
		if previewMode {
			if chosen < len(diags) {
				// diagnostic preview
				d := diags[chosen]
				if b, err := previewAutoFix(d); err == nil {
					showDiff(d.FilePath, b)
					// If auto-apply is enabled, do not prompt; otherwise ask the user.
					applyNow := false
					if globalAutoApply {
						applyNow = true
					} else {
						fmt.Print("Apply this preview? (y/N): ")
						yn, _ := reader.ReadString('\n')
						yn = strings.TrimSpace(strings.ToLower(yn))
						if yn == "y" || yn == "yes" {
							applyNow = true
						}
					}
					if applyNow {
						// perform same apply steps as normal flow for this diagnostic
						if _, ok := backups[d.FilePath]; !ok {
							if bb, err := os.ReadFile(d.FilePath); err == nil {
								backups[d.FilePath] = bb
							} else {
								backups[d.FilePath] = nil
							}
						}
						fmt.Printf("Applying auto-fix to %s...\n", d.FilePath)
						if generator.AutoFixFile(d) {
							modified[d.FilePath] = true
							appliedAny = true
							fmt.Printf("Auto-fix applied to %s\n", d.FilePath)
						} else {
							fmt.Printf("Auto-fix failed for %s\n", d.FilePath)
						}
					}
				} else {
					fmt.Printf("preview failed for %s: %v\n", d.FilePath, err)
				}
			} else {
				optIdx := chosen - len(diags)
				if optIdx >= 0 && optIdx < len(opts) {
					o := opts[optIdx]
					if o.kind == "add_context" {
						if b, err := previewAddContext(o.file, o.symbol, dir); err == nil {
							showDiff(o.file, b)
						} else {
							fmt.Printf("preview failed for %s: %v\n", o.file, err)
						}
					}
				}
			}
			continue
		}
		// diagnostic choices
		if chosen < len(diags) {
			d := diags[chosen]
			target := d.FilePath
			if _, ok := backups[target]; !ok {
				if b, err := os.ReadFile(target); err == nil {
					backups[target] = b
				} else {
					backups[target] = nil
				}
			}
			fmt.Printf("Applying auto-fix to %s...\n", target)
			if generator.AutoFixFile(d) {
				modified[target] = true
				appliedAny = true
				fmt.Printf("Auto-fix applied to %s\n", target)
			} else {
				// If the generic fixer failed, and this looks like a missing '{'
				// after a `type X struct` line, try a conservative inline edit
				// (insert '{' at end of previous line). This is safe and can be
				// rolled back by restoring backups on verification failure.
				if globalAutoApply && strings.Contains(d.Message, "expected {") {
					data, rerr := os.ReadFile(target)
					if rerr == nil {
						lines := strings.Split(string(data), "\n")
						ln := d.Line - 1
						prev := ln - 1
						if prev >= 0 && prev < len(lines) {
							trim := strings.TrimSpace(lines[prev])
							if strings.HasPrefix(trim, "type ") && strings.Contains(trim, "struct") && !strings.Contains(lines[prev], "{") {
								lines[prev] = lines[prev] + " {"
								_ = os.WriteFile(target, []byte(strings.Join(lines, "\n")), 0644)
								modified[target] = true
								appliedAny = true
								fmt.Printf("Conservative inline edit applied to %s\n", target)
								goto applied_diag
							}
						}
					}
				}
				fmt.Printf("Auto-fix failed for %s\n", target)
			}
		applied_diag:
			continue
		}

		// otherwise choose from heuristic opts
		optIdx := chosen - len(diags)
		if optIdx >= 0 && optIdx < len(opts) {
			o := opts[optIdx]
			if o.kind == "add_context" {
				targetFile := o.file
				if _, ok := backups[targetFile]; !ok {
					if b, err := os.ReadFile(targetFile); err == nil {
						backups[targetFile] = b
					} else {
						backups[targetFile] = nil
					}
				}
				fmt.Printf("Injecting context param into %s in %s...\n", o.symbol, targetFile)
				wgraph2, err := dense.IndexWorkspace(dir)
				if err != nil {
					fmt.Printf("failed to index workspace: %v\n", err)
					continue
				}
				slot := dense.CodeAwareSlot{}
				slot.ParsedSlot.Action = "ADD_CONTEXT"
				slot.ParsedSlot.TargetSymbol = o.symbol
				applied, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(wgraph2, "", slot)
				if !ok {
					fmt.Printf("failed to apply mutation for %s\n", o.symbol)
					continue
				}
				fset := wgraph2.Fsets[applied]
				node := wgraph2.Files[applied]
				if err := writeFormattedFile(applied, fset, node); err != nil {
					fmt.Printf("failed to write file %s: %v\n", applied, err)
					continue
				}
				modified[applied] = true
				appliedAny = true
				fmt.Printf("Applied mutation to %s\n", applied)
			}
		}
	}

	if !appliedAny {
		fmt.Println("No changes applied.")
		fmt.Println("=== dense: suggestion pass complete ===")
		return
	}

	// run `go test` to verify the workspace; if it fails, roll back
	fmt.Println("Verifying changes by running 'go test ./...'...")
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	var outb strings.Builder
	cmd.Stdout = &outb
	cmd.Stderr = &outb
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Verification failed: %v\nOutput:\n%s\nRolling back changes...\n", err, outb.String())
		// restore backups
		for fp, content := range backups {
			if content == nil {
				// no in-memory backup; if auto-restore from git is enabled, try that
				if globalAutoRestoreGit {
					cmd := exec.Command("git", "checkout", "--", fp)
					cmd.Dir = dir
					if rerr := cmd.Run(); rerr == nil {
						fmt.Printf("Restored %s from git HEAD\n", fp)
						continue
					}
				}
				// otherwise skip
				continue
			}
			_ = os.WriteFile(fp, content, 0644)
			fmt.Printf("Restored %s\n", fp)
		}
		fmt.Println("Rollback complete.")
	} else {
		fmt.Println("Verification passed. Changes kept.")
		// remove backup entries (in-memory) - nothing to do on disk
	}

	fmt.Println("=== dense: suggestion pass complete ===")
}

func writeFormattedFile(filePath string, fset *token.FileSet, node *ast.File) error {
	var sb strings.Builder
	if err := format.Node(&sb, fset, node); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(sb.String()), 0644)
}

func hasContextParam(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return false
	}
	for _, f := range fn.Type.Params.List {
		// look for selectorexpr context.Context or ident named Context
		switch t := f.Type.(type) {
		case *ast.SelectorExpr:
			if id, ok := t.X.(*ast.Ident); ok && id.Name == "context" && t.Sel.Name == "Context" {
				return true
			}
		case *ast.Ident:
			if t.Name == "Context" {
				return true
			}
		}
	}
	return false
}
