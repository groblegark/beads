package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/runbook"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// runbookCmd is the parent command for runbook operations.
var runbookCmd = &cobra.Command{
	Use:   "runbook",
	Short: "Manage OJ runbook definitions stored as beads",
	Long: `Manage OJ runbook definitions stored as beads.

Runbook beads store the full content of OJ runbook files (HCL/TOML/JSON)
in the beads database, enabling versioning, sharing, and materialization.

Scope hierarchy (most-specific wins for same-name runbooks):
  global           Available everywhere (specificity 0)
  town:<name>      Available within a town (specificity 1)
  rig:<name>       Available within a rig (specificity 2)
  role:<name>      Available to a specific role (specificity 3)
  agent:<name>     Available to a specific agent (specificity 4)

Search paths for filesystem runbooks:
  1. .oj/runbooks/ (project)
  2. library/ (project libraries)

Commands:
  list         List runbook beads from database and/or filesystem
  show         Show runbook details
  create       Create a runbook bead from a file
  import       Batch import runbooks from filesystem into database
  materialize  Write runbook beads to filesystem`,
}

// runbookListCmd lists all runbook beads.
var runbookListCmd = &cobra.Command{
	Use:   "list",
	Short: "List runbook beads",
	Long: `List runbook beads from database and/or filesystem.

Use --source to control where runbooks are listed from:
  --source=all    List from both database and filesystem (default)
  --source=db     List only runbooks stored in database
  --source=files  List only runbooks from filesystem

Use --all to discover runbooks across all rigs in a Gas Town workspace.
Use --scope to filter by scope level (global, rig, role, etc.).

Examples:
  bd runbook list
  bd runbook list --json
  bd runbook list --source=db
  bd runbook list --scope=rig:beads
  bd runbook list --scope=global
  bd runbook list --all`,
	Run: runRunbookList,
}

// runbookShowCmd shows details of a specific runbook.
var runbookShowCmd = &cobra.Command{
	Use:   "show <name|bead-id>",
	Short: "Show runbook details",
	Long: `Show detailed information about a runbook.

When given a bead ID (e.g., od-runbook-base), loads from database.
When given a runbook name, searches filesystem and database.

Displays:
  - Metadata (name, format, source)
  - Jobs, commands, workers, crons defined
  - Full runbook content (with --content flag)

Examples:
  bd runbook show base
  bd runbook show od-runbook-base
  bd runbook show base --content
  bd runbook show base --json`,
	Args: cobra.ExactArgs(1),
	Run:  runRunbookShow,
}

// runbookCreateCmd creates a runbook bead from a file.
var runbookCreateCmd = &cobra.Command{
	Use:   "create <name> --file=<path>",
	Short: "Create a runbook bead from a file",
	Long: `Create a runbook bead by importing a runbook file into the database.

The full file content is stored in the bead's metadata, enabling
lossless roundtrip with 'bd runbook materialize'.

Examples:
  bd runbook create base --file=.oj/runbooks/base.hcl
  bd runbook create my-runbook --file=path/to/runbook.hcl
  bd runbook create specs --file=.oj/runbooks/specs.hcl --force`,
	Args: cobra.ExactArgs(1),
	Run:  runRunbookCreate,
}

// runbookImportCmd batch-imports runbooks from filesystem.
var runbookImportCmd = &cobra.Command{
	Use:   "import [name|path]",
	Short: "Import runbooks from filesystem into database",
	Long: `Import runbook files from .oj/runbooks/ and library/ directories
into the beads database as TypeRunbook issues.

Examples:
  bd runbook import base.hcl            # Import specific file
  bd runbook import --all               # Import all from search paths
  bd runbook import --all --force       # Re-import all (overwrite existing)
  bd runbook import --dir=library/wok   # Import from specific directory`,
	Run: runRunbookImport,
}

// runbookMaterializeCmd writes runbook beads to the filesystem.
var runbookMaterializeCmd = &cobra.Command{
	Use:   "materialize [name|bead-id]",
	Short: "Write runbook beads to filesystem as .oj/runbooks/ files",
	Long: `Materialize runbook beads from the database to the filesystem.

This is the reverse of 'bd runbook import': it reads runbook content from
beads and writes them as files to .oj/runbooks/ for the OJ daemon to use.

When multiple runbooks share the same name, the most-specific scope wins:
  agent > role > rig > town > global

Use --scope to only materialize runbooks matching a specific scope level.

Examples:
  bd runbook materialize base              # Write single runbook
  bd runbook materialize --all             # Write all (most-specific-wins)
  bd runbook materialize --all --dry-run   # Preview without writing
  bd runbook materialize base --dir=/tmp   # Write to custom directory
  bd runbook materialize --all --force     # Overwrite existing files
  bd runbook materialize --all --scope=rig:beads  # Only rig-scoped runbooks`,
	Run: runRunbookMaterialize,
}

// RunbookListEntry represents a runbook in list output.
type RunbookListEntry struct {
	Name     string `json:"name"`
	Format   string `json:"format"`
	Source   string `json:"source"`
	Scope    string `json:"scope"`
	Jobs     int    `json:"jobs"`
	Commands int    `json:"commands"`
	Workers  int    `json:"workers"`
}

var (
	rbListSource       string
	rbListAllRigs      bool
	rbListScope        string
	rbShowContent      bool
	rbCreateFile       string
	rbCreateForce      bool
	rbCreateScope      string
	rbImportAll        bool
	rbImportForce      bool
	rbImportDir        string
	rbImportScope      string
	rbMaterializeAll   bool
	rbMaterializeForce bool
	rbMaterializeDry   bool
	rbMaterializeDir   string
	rbMaterializeScope string
)

func runRunbookList(cmd *cobra.Command, args []string) {
	seen := make(map[string]bool)
	var entries []RunbookListEntry

	source := rbListSource
	if source == "" {
		source = "all"
	}

	if rbListAllRigs {
		runRunbookListAll()
		return
	}

	// Load from database
	if source == "all" || source == "db" {
		dbEntries := listRunbooksFromDB()
		for _, e := range dbEntries {
			if !seen[e.Name] {
				seen[e.Name] = true
				entries = append(entries, e)
			}
		}
	}

	// Load from filesystem
	if source == "all" || source == "files" {
		searchPaths := getRunbookSearchPaths()
		for _, dir := range searchPaths {
			files := scanRunbookDir(dir)
			for _, rb := range files {
				if seen[rb.Name] {
					continue
				}
				seen[rb.Name] = true
				entries = append(entries, RunbookListEntry{
					Name:     rb.Name,
					Format:   rb.Format,
					Source:   rb.Source,
					Jobs:     len(rb.Jobs),
					Commands: len(rb.Commands),
					Workers:  len(rb.Workers),
				})
			}
		}
	}

	// Apply --scope filter if provided
	if rbListScope != "" {
		var filtered []RunbookListEntry
		for _, e := range entries {
			if matchesScope(e.Scope, rbListScope) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	if jsonOutput {
		outputJSON(entries)
		return
	}

	if len(entries) == 0 {
		fmt.Println("No runbooks found.")
		if rbListScope != "" {
			fmt.Printf("  (filtered by scope: %s)\n", rbListScope)
		}
		if source == "all" || source == "files" {
			fmt.Println("\nSearch paths:")
			for _, p := range getRunbookSearchPaths() {
				fmt.Printf("  %s\n", p)
			}
		}
		return
	}

	fmt.Printf("Runbooks (%d found)\n\n", len(entries))
	fmt.Printf("  %-25s %-6s %-8s %-18s %s\n", "NAME", "FMT", "SOURCE", "SCOPE", "CONTENTS")
	fmt.Printf("  %-25s %-6s %-8s %-18s %s\n", "----", "---", "------", "-----", "--------")
	for _, e := range entries {
		var parts []string
		if e.Jobs > 0 {
			parts = append(parts, fmt.Sprintf("%d jobs", e.Jobs))
		}
		if e.Commands > 0 {
			parts = append(parts, fmt.Sprintf("%d cmds", e.Commands))
		}
		if e.Workers > 0 {
			parts = append(parts, fmt.Sprintf("%d workers", e.Workers))
		}
		contents := strings.Join(parts, ", ")
		if contents == "" {
			contents = "-"
		}
		scope := e.Scope
		if scope == "" {
			scope = "global"
		}
		fmt.Printf("  %-25s %-6s %-8s %-18s %s\n", e.Name, e.Format, e.Source, scope, contents)
	}
}

func runRunbookListAll() {
	gtRoot := os.Getenv("GT_ROOT")
	if gtRoot == "" {
		fmt.Fprintf(os.Stderr, "Error: --all requires GT_ROOT to be set\n")
		fmt.Fprintf(os.Stderr, "This flag discovers runbooks across all rigs in a Gas Town workspace.\n")
		os.Exit(1)
	}

	type rigGroup struct {
		Rig     string             `json:"rig"`
		Entries []RunbookListEntry `json:"entries"`
	}

	townBeadsDir := filepath.Join(gtRoot, ".beads")
	routes, err := routing.LoadRoutes(townBeadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading routes: %v\n", err)
		os.Exit(1)
	}

	var groups []rigGroup
	for _, route := range routes {
		if route.Path == "." {
			continue
		}

		rigName := routing.ExtractProjectFromPath(route.Path)
		if rigName == "" {
			rigName = route.Prefix
		}

		dir := filepath.Join(gtRoot, route.Path, ".oj", "runbooks")
		files := scanRunbookDir(dir)
		if len(files) == 0 {
			continue
		}
		var entries []RunbookListEntry
		for _, rb := range files {
			// Filesystem runbooks discovered via --all are inferred as rig-scoped
			scope := rb.Scope
			if scope == "" {
				scope = "rig:" + rigName
			}
			entries = append(entries, RunbookListEntry{
				Name:     rb.Name,
				Format:   rb.Format,
				Source:   "file",
				Scope:    scope,
				Jobs:     len(rb.Jobs),
				Commands: len(rb.Commands),
				Workers:  len(rb.Workers),
			})
		}
		groups = append(groups, rigGroup{Rig: rigName, Entries: entries})
	}

	if jsonOutput {
		outputJSON(groups)
		return
	}

	if len(groups) == 0 {
		fmt.Println("No runbooks found across rigs.")
		return
	}

	for _, g := range groups {
		fmt.Printf("%s (%d runbooks)\n", g.Rig, len(g.Entries))
		for _, e := range g.Entries {
			scope := e.Scope
			if scope == "" {
				scope = "global"
			}
			fmt.Printf("  %-25s %-6s %-18s %d jobs, %d cmds\n", e.Name, e.Format, scope, e.Jobs, e.Commands)
		}
		fmt.Println()
	}
}

func runRunbookShow(cmd *cobra.Command, args []string) {
	nameOrID := args[0]

	// Try loading from database first
	rb := loadRunbookFromDB(nameOrID)

	// Fall back to filesystem
	if rb == nil {
		rb = loadRunbookFromFiles(nameOrID)
	}

	if rb == nil {
		fmt.Fprintf(os.Stderr, "Error: runbook %q not found in database or filesystem\n", nameOrID)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(rb)
		return
	}

	// Display
	fmt.Printf("%s %s\n", ui.RenderBold("Runbook:"), rb.Name)
	fmt.Printf("  Format: %s\n", rb.Format)
	if rb.Source != "" {
		fmt.Printf("  Source: %s\n", rb.Source)
	}
	scope := rb.Scope
	if scope == "" {
		scope = "global"
	}
	fmt.Printf("  Scope:  %s (specificity: %d)\n", scope, runbook.ScopeSpecificity(scope))
	fmt.Println()

	if len(rb.Jobs) > 0 {
		fmt.Printf("  Jobs (%d):\n", len(rb.Jobs))
		for _, j := range rb.Jobs {
			fmt.Printf("    - %s\n", j)
		}
	}
	if len(rb.Commands) > 0 {
		fmt.Printf("  Commands (%d):\n", len(rb.Commands))
		for _, c := range rb.Commands {
			fmt.Printf("    - %s\n", c)
		}
	}
	if len(rb.Workers) > 0 {
		fmt.Printf("  Workers (%d):\n", len(rb.Workers))
		for _, w := range rb.Workers {
			fmt.Printf("    - %s\n", w)
		}
	}
	if len(rb.Crons) > 0 {
		fmt.Printf("  Crons (%d):\n", len(rb.Crons))
		for _, c := range rb.Crons {
			fmt.Printf("    - %s\n", c)
		}
	}
	if len(rb.Queues) > 0 {
		fmt.Printf("  Queues (%d):\n", len(rb.Queues))
		for _, q := range rb.Queues {
			fmt.Printf("    - %s\n", q)
		}
	}

	if rbShowContent {
		fmt.Println()
		fmt.Println(ui.RenderBold("Content:"))
		fmt.Println(rb.Content)
	}
}

func runRunbookCreate(cmd *cobra.Command, args []string) {
	CheckReadonly("runbook create")

	name := args[0]
	if rbCreateFile == "" {
		fmt.Fprintf(os.Stderr, "Error: --file flag is required\n")
		os.Exit(1)
	}

	content, err := os.ReadFile(rbCreateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file %q: %v\n", rbCreateFile, err)
		os.Exit(1)
	}

	format := detectFormat(rbCreateFile)
	rb := runbook.ParseRunbookFile(name, string(content), format)
	rb.Source = rbCreateFile
	if rbCreateScope != "" {
		rb.Scope = rbCreateScope
	}

	result, err := saveRunbookToDB(rb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating runbook %q: %v\n", name, err)
		os.Exit(1)
	}

	action := "Created"
	if !result.created {
		action = "Updated"
	}
	scopeInfo := ""
	if rb.Scope != "" {
		scopeInfo = fmt.Sprintf(" (scope: %s)", rb.Scope)
	}
	fmt.Printf("%s %s runbook %q as %s%s\n", ui.RenderPass("✓"), action, name, result.id, scopeInfo)
}

func runRunbookImport(cmd *cobra.Command, args []string) {
	CheckReadonly("runbook import")

	if !rbImportAll && len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Error: runbook name/path required (or use --all)\n")
		os.Exit(1)
	}

	if rbImportAll {
		importAllRunbooks()
		return
	}

	// Import single file
	path := args[0]
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %q: %v\n", path, err)
		os.Exit(1)
	}

	format := detectFormat(path)
	rb := runbook.ParseRunbookFile(name, string(content), format)
	rb.Source = path
	if rbImportScope != "" {
		rb.Scope = rbImportScope
	}

	result, err := saveRunbookToDB(rb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error importing %q: %v\n", name, err)
		os.Exit(1)
	}

	action := "Created"
	if !result.created {
		action = "Updated"
	}
	fmt.Printf("%s %s runbook %q as %s\n", ui.RenderPass("✓"), action, name, result.id)
}

func importAllRunbooks() {
	searchPaths := getRunbookSearchPaths()
	if rbImportDir != "" {
		searchPaths = []string{rbImportDir}
	}

	seen := make(map[string]bool)
	imported := 0
	skipped := 0
	errors := 0

	for _, dir := range searchPaths {
		files := scanRunbookDir(dir)
		for _, rb := range files {
			if seen[rb.Name] {
				skipped++
				continue
			}
			seen[rb.Name] = true

			if rbImportScope != "" {
				rb.Scope = rbImportScope
			}

			result, err := saveRunbookToDB(rb)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s %s: %v\n", ui.RenderFail("✗"), rb.Name, err)
				errors++
				continue
			}

			if result.created {
				fmt.Printf("  %s Imported %s → %s\n", ui.RenderPass("✓"), rb.Name, result.id)
			} else {
				fmt.Printf("  %s Updated %s → %s\n", ui.RenderPass("✓"), rb.Name, result.id)
			}
			imported++
		}
	}

	fmt.Printf("\nImported: %d, Skipped: %d, Errors: %d\n", imported, skipped, errors)
}

func runRunbookMaterialize(cmd *cobra.Command, args []string) {
	CheckReadonly("runbook materialize")

	if !rbMaterializeAll && len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Error: runbook name/id required (or use --all)\n")
		os.Exit(1)
	}

	// Determine output directory
	outDir := rbMaterializeDir
	if outDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %v\n", err)
			os.Exit(1)
		}
		outDir = filepath.Join(cwd, ".oj", "runbooks")
	}

	if rbMaterializeAll {
		materializeAll(outDir)
		return
	}

	// Single runbook
	nameOrID := args[0]
	rb := loadRunbookFromDB(nameOrID)
	if rb == nil {
		fmt.Fprintf(os.Stderr, "Error: runbook %q not found in database\n", nameOrID)
		os.Exit(1)
	}

	err := materializeOne(rb, outDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func materializeAll(outDir string) {
	s := getStore()
	if s == nil {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		os.Exit(1)
	}

	ctx := context.Background()
	filter := types.IssueFilter{
		IssueType: func() *types.IssueType { t := types.TypeRunbook; return &t }(),
	}
	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying runbooks: %v\n", err)
		os.Exit(1)
	}

	// Batch-load labels for scope resolution
	var issueIDs []string
	for _, issue := range issues {
		if issue.Status != types.StatusClosed {
			issueIDs = append(issueIDs, issue.ID)
		}
	}
	labelsMap := make(map[string][]string)
	if len(issueIDs) > 0 {
		if lm, err := s.GetLabelsForIssues(ctx, issueIDs); err == nil {
			labelsMap = lm
		}
	}

	// Collect all runbooks with their scope, then resolve by most-specific-wins
	type scopedRunbook struct {
		rb    *runbook.RunbookContent
		scope string
	}

	bestByName := make(map[string]*scopedRunbook)
	for _, issue := range issues {
		if issue.Status == types.StatusClosed {
			continue
		}
		rb, err := runbook.IssueToRunbook(issue)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s: %v\n", ui.RenderFail("✗"), issue.ID, err)
			continue
		}

		scope := runbook.ParseScopeFromLabels(labelsMap[issue.ID])

		// Apply --scope filter if provided
		if rbMaterializeScope != "" && !matchesScope(scope, rbMaterializeScope) {
			continue
		}

		// Most-specific-wins: keep the runbook with highest specificity for each name
		spec := runbook.ScopeSpecificity(scope)
		if existing, ok := bestByName[rb.Name]; !ok || spec > runbook.ScopeSpecificity(existing.scope) {
			bestByName[rb.Name] = &scopedRunbook{rb: rb, scope: scope}
		}
	}

	written := 0
	skipped := 0
	errors := 0

	for _, sr := range bestByName {
		err := materializeOne(sr.rb, outDir)
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				skipped++
				if !rbMaterializeDry {
					fmt.Fprintf(os.Stderr, "  skipped %s (exists, use --force)\n", sr.rb.Name)
				}
			} else {
				fmt.Fprintf(os.Stderr, "  %s %s: %v\n", ui.RenderFail("✗"), sr.rb.Name, err)
				errors++
			}
			continue
		}
		written++
	}

	fmt.Printf("\nMaterialized: %d, Skipped: %d, Errors: %d\n", written, skipped, errors)
}

func materializeOne(rb *runbook.RunbookContent, outDir string) error {
	ext := "." + rb.Format
	if rb.Format == "" {
		ext = ".hcl"
	}
	filename := rb.Name + ext
	outPath := filepath.Join(outDir, filename)

	if rbMaterializeDry {
		fmt.Printf("  [dry-run] Would write %s (%d bytes)\n", outPath, len(rb.Content))
		return nil
	}

	// Check if file exists
	if !rbMaterializeForce {
		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", outDir, err)
	}

	if err := os.WriteFile(outPath, []byte(rb.Content), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}

	fmt.Printf("  %s %s → %s\n", ui.RenderPass("✓"), rb.Name, outPath)
	return nil
}

// --- Scope helpers ---

// matchesScope checks if a runbook's scope matches the given filter.
// Exact match or prefix match (e.g., filter "rig:" matches "rig:beads").
func matchesScope(entryScope, filter string) bool {
	if filter == "" {
		return true
	}
	if entryScope == "" {
		entryScope = "global"
	}
	if entryScope == filter {
		return true
	}
	// Allow prefix matching: --scope=rig matches all rig:X scopes
	if !strings.Contains(filter, ":") {
		return strings.HasPrefix(entryScope, filter+":")  || entryScope == filter
	}
	return false
}

// resolveRunbooksByScope takes a list of runbook entries and resolves
// name conflicts using most-specific-wins. When multiple runbooks share
// a name, the one with the highest specificity score wins.
func resolveRunbooksByScope(entries []RunbookListEntry) []RunbookListEntry {
	bestByName := make(map[string]RunbookListEntry)
	bestSpec := make(map[string]int)

	for _, e := range entries {
		scope := e.Scope
		if scope == "" {
			scope = "global"
		}
		spec := runbook.ScopeSpecificity(scope)
		if existing, ok := bestByName[e.Name]; !ok || spec > bestSpec[e.Name] {
			_ = existing
			bestByName[e.Name] = e
			bestSpec[e.Name] = spec
		}
	}

	result := make([]RunbookListEntry, 0, len(bestByName))
	for _, e := range bestByName {
		result = append(result, e)
	}
	return result
}

// --- Helper functions ---

// listRunbooksFromDB queries the database for runbook-type issues.
func listRunbooksFromDB() []RunbookListEntry {
	s := getStore()
	if s == nil {
		return nil
	}

	ctx := context.Background()
	filter := types.IssueFilter{
		IssueType: func() *types.IssueType { t := types.TypeRunbook; return &t }(),
	}
	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil
	}

	// Batch-load labels for scope resolution
	var issueIDs []string
	for _, issue := range issues {
		if issue.Status != types.StatusClosed {
			issueIDs = append(issueIDs, issue.ID)
		}
	}
	labelsMap := make(map[string][]string)
	if len(issueIDs) > 0 {
		if lm, err := s.GetLabelsForIssues(ctx, issueIDs); err == nil {
			labelsMap = lm
		}
	}

	var entries []RunbookListEntry
	for _, issue := range issues {
		if issue.Status == types.StatusClosed {
			continue
		}

		rb, err := runbook.IssueToRunbook(issue)
		if err != nil {
			continue
		}

		scope := runbook.ParseScopeFromLabels(labelsMap[issue.ID])

		entries = append(entries, RunbookListEntry{
			Name:     rb.Name,
			Format:   rb.Format,
			Source:   "db",
			Scope:    scope,
			Jobs:     len(rb.Jobs),
			Commands: len(rb.Commands),
			Workers:  len(rb.Workers),
		})
	}

	return entries
}

// loadRunbookFromDB loads a runbook from the database by ID or name.
func loadRunbookFromDB(nameOrID string) *runbook.RunbookContent {
	s := getStore()
	if s == nil {
		return nil
	}

	ctx := context.Background()

	// Try direct ID lookup
	issue, err := s.GetIssue(ctx, nameOrID)
	if err == nil && issue != nil && issue.IssueType == types.TypeRunbook {
		rb, err := runbook.IssueToRunbook(issue)
		if err == nil {
			return rb
		}
	}

	// Try by slug-based ID
	idPrefix := ""
	if p, err := s.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
		idPrefix = p + "-"
	}
	slug := "runbook-" + strings.ToLower(strings.ReplaceAll(nameOrID, " ", "-"))
	candidateID := idPrefix + slug
	issue, err = s.GetIssue(ctx, candidateID)
	if err == nil && issue != nil && issue.IssueType == types.TypeRunbook {
		rb, err := runbook.IssueToRunbook(issue)
		if err == nil {
			return rb
		}
	}

	// Search by title
	filter := types.IssueFilter{
		IssueType: func() *types.IssueType { t := types.TypeRunbook; return &t }(),
	}
	issues, err := s.SearchIssues(ctx, nameOrID, filter)
	if err != nil {
		return nil
	}
	for _, iss := range issues {
		if strings.EqualFold(iss.Title, nameOrID) {
			rb, err := runbook.IssueToRunbook(iss)
			if err == nil {
				return rb
			}
		}
	}

	return nil
}

// loadRunbookFromFiles searches the filesystem for a runbook by name.
func loadRunbookFromFiles(name string) *runbook.RunbookContent {
	for _, dir := range getRunbookSearchPaths() {
		files := scanRunbookDir(dir)
		for _, rb := range files {
			if rb.Name == name {
				return rb
			}
		}
	}
	return nil
}

// getRunbookSearchPaths returns directories to scan for runbook files.
func getRunbookSearchPaths() []string {
	var paths []string

	// 1. Project .oj/runbooks/
	cwd, err := os.Getwd()
	if err == nil {
		paths = append(paths, filepath.Join(cwd, ".oj", "runbooks"))
	}

	// 2. Project library/ subdirectories
	if err == nil {
		libDir := filepath.Join(cwd, "library")
		entries, err := os.ReadDir(libDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					paths = append(paths, filepath.Join(libDir, e.Name()))
				}
			}
		}
	}

	// 3. GT_ROOT-based paths (if available)
	gtRoot := os.Getenv("GT_ROOT")
	if gtRoot != "" {
		paths = append(paths, filepath.Join(gtRoot, ".oj", "runbooks"))
	}

	return paths
}

// scanRunbookDir scans a directory for runbook files.
func scanRunbookDir(dir string) []*runbook.RunbookContent {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var runbooks []*runbook.RunbookContent
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".hcl" && ext != ".toml" && ext != ".json" {
			continue
		}

		path := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		name := strings.TrimSuffix(e.Name(), ext)
		format := strings.TrimPrefix(ext, ".")
		rb := runbook.ParseRunbookFile(name, string(content), format)
		rb.Source = path
		runbooks = append(runbooks, rb)
	}

	return runbooks
}

// detectFormat determines the file format from the extension.
func detectFormat(path string) string {
	ext := filepath.Ext(path)
	switch ext {
	case ".hcl":
		return "hcl"
	case ".toml":
		return "toml"
	case ".json":
		return "json"
	default:
		return "hcl" // Default to HCL
	}
}

type runbookSaveResult struct {
	id      string
	created bool
}

// saveRunbookToDB saves a runbook to the database.
func saveRunbookToDB(rb *runbook.RunbookContent) (*runbookSaveResult, error) {
	s := getStore()
	if s == nil {
		return nil, fmt.Errorf("no database connection (set BD_DAEMON_HOST or run in a beads directory)")
	}

	ctx := rootCtx

	idPrefix := ""
	if p, err := s.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
		idPrefix = p + "-"
	}

	issue, labels, err := runbook.RunbookToIssue(rb, idPrefix)
	if err != nil {
		return nil, fmt.Errorf("converting runbook to issue: %w", err)
	}

	if issue.Status == "" {
		issue.Status = "open"
	}

	// Check if exists
	existing, _ := s.GetIssue(ctx, issue.ID)
	created := existing == nil

	if existing != nil {
		if !rbCreateForce && !rbImportForce {
			return nil, fmt.Errorf("runbook %q already exists as %s (use --force to overwrite)", rb.Name, issue.ID)
		}
		// Re-serialize metadata in case content changed
		metadataBytes, err := json.Marshal(rb)
		if err != nil {
			return nil, fmt.Errorf("serializing runbook: %w", err)
		}
		updates := map[string]interface{}{
			"title":       issue.Title,
			"description": issue.Description,
			"metadata":    json.RawMessage(metadataBytes),
		}
		if err := s.UpdateIssue(ctx, existing.ID, updates, actor); err != nil {
			return nil, fmt.Errorf("updating runbook: %w", err)
		}
	} else {
		if err := s.CreateIssue(ctx, issue, actor); err != nil {
			return nil, fmt.Errorf("creating runbook: %w", err)
		}
	}

	for _, label := range labels {
		_ = s.AddLabel(ctx, issue.ID, label, actor)
	}

	return &runbookSaveResult{
		id:      issue.ID,
		created: created,
	}, nil
}

func init() {
	runbookListCmd.Flags().StringVar(&rbListSource, "source", "all", "Source to list from: db, files, or all (default)")
	runbookListCmd.Flags().BoolVar(&rbListAllRigs, "all", false, "Discover runbooks across all rigs (requires GT_ROOT)")
	runbookListCmd.Flags().StringVar(&rbListScope, "scope", "", "Filter by scope (global, town:X, rig:X, role:X, agent:X)")

	runbookShowCmd.Flags().BoolVar(&rbShowContent, "content", false, "Show full runbook file content")

	runbookCreateCmd.Flags().StringVar(&rbCreateFile, "file", "", "Path to runbook file (required)")
	runbookCreateCmd.Flags().BoolVar(&rbCreateForce, "force", false, "Overwrite existing runbook in database")
	runbookCreateCmd.Flags().StringVar(&rbCreateScope, "scope", "", "Scope level: global, town:X, rig:X, role:X, agent:X")

	runbookImportCmd.Flags().BoolVar(&rbImportAll, "all", false, "Import all runbooks from search paths")
	runbookImportCmd.Flags().BoolVar(&rbImportForce, "force", false, "Overwrite existing runbooks in database")
	runbookImportCmd.Flags().StringVar(&rbImportDir, "dir", "", "Import from specific directory")
	runbookImportCmd.Flags().StringVar(&rbImportScope, "scope", "", "Scope level for imported runbooks: global, town:X, rig:X, role:X, agent:X")

	runbookMaterializeCmd.Flags().BoolVar(&rbMaterializeAll, "all", false, "Materialize all runbook beads")
	runbookMaterializeCmd.Flags().BoolVar(&rbMaterializeForce, "force", false, "Overwrite existing files")
	runbookMaterializeCmd.Flags().BoolVar(&rbMaterializeDry, "dry-run", false, "Preview without writing files")
	runbookMaterializeCmd.Flags().StringVar(&rbMaterializeDir, "dir", "", "Output directory (default: .oj/runbooks/)")
	runbookMaterializeCmd.Flags().StringVar(&rbMaterializeScope, "scope", "", "Filter by scope (global, town:X, rig:X, role:X, agent:X)")

	runbookCmd.AddCommand(runbookListCmd)
	runbookCmd.AddCommand(runbookShowCmd)
	runbookCmd.AddCommand(runbookCreateCmd)
	runbookCmd.AddCommand(runbookImportCmd)
	runbookCmd.AddCommand(runbookMaterializeCmd)
	rootCmd.AddCommand(runbookCmd)
}
