package team

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// boardDBFile is the SQLite board name shared by the durable command chain; the
// adoption copies it whole when a legacy project had one.
const boardDBFile = "board.db"

// adoptedMarkerFile records the legacy project team data dirs fully adopted
// into a user-global store, so an open never re-adopts (and re-resurrects)
// teams the user has since deleted.
const adoptedMarkerFile = ".adopted.json"

// adoptedMarker is .adopted.json's shape: each source dir folded in exactly once.
type adoptedMarker struct {
	Document
	Sources []string `json:"sources"`
}

// AdoptOptions controls one AdoptProjectInto call.
type AdoptOptions struct {
	// AllowLegacy lets a project's .reasonix/team be read as a legacy source.
	// Hosts clear it when REASONIX_HOME is explicitly set, so an isolated
	// instance never imports another install's team data.
	AllowLegacy bool
}

// AdoptReport summarises one adoption: the source/target dirs and how many
// teams, pool entries, and member histories moved. Skipped names why nothing
// moved (isolated home, no legacy data, already adopted, same dir).
type AdoptReport struct {
	Source            string
	Target            string
	Skipped           string
	TeamsCreated      int
	TeamsSkipped      int
	AgentUsersCreated int
	HistoriesAdopted  int
	BoardAdopted      bool
}

// AdoptProjectInto folds a legacy project .reasonix/team tree (projectDir)
// into a user-global team data dir (userDir), exactly once per source
// (recorded in .adopted.json). The source is never modified or deleted.
// Registry and pool merge by name/id; a team already present in the user store
// — and its history — is left untouched, so adoption never clobbers global
// data. A team is added only after its history is copied, so an interrupted
// adoption re-copies on the next attempt instead of leaving a registry team
// with no history.
func AdoptProjectInto(userDir, projectDir string, opt AdoptOptions) (AdoptReport, error) {
	rep := AdoptReport{Source: projectDir, Target: userDir}
	if !opt.AllowLegacy {
		rep.Skipped = "legacy disabled: REASONIX_HOME is explicitly set"
		return rep, nil
	}
	if userDir == "" || projectDir == "" {
		rep.Skipped = "no data dir"
		return rep, nil
	}
	if sameDir(userDir, projectDir) {
		rep.Skipped = "legacy and user store share one dir"
		return rep, nil
	}
	src, err := loadTeamDocAt(projectDir)
	if err != nil {
		return rep, err
	}
	if len(src.Teams) == 0 {
		rep.Skipped = "no legacy teams"
		return rep, nil
	}
	if markerHas(userDir, projectDir) {
		rep.Skipped = "already adopted"
		return rep, nil
	}
	dst, err := NewTeamStoreAt("", userDir)
	if err != nil {
		return rep, err
	}
	dstDoc, err := loadTeamDocAt(userDir)
	if err != nil {
		return rep, err
	}
	have := make(map[string]bool, len(dstDoc.Teams))
	for _, t := range dstDoc.Teams {
		have[t.Name] = true
	}
	for i := range src.Teams {
		name := src.Teams[i].Name
		if have[name] {
			rep.TeamsSkipped++
			continue
		}
		adopted, err := adoptTeamHistory(projectDir, userDir, name)
		if err != nil {
			return rep, err
		}
		if adopted {
			rep.HistoriesAdopted++
		}
		if err := dst.AddTeam(src.Teams[i]); err != nil {
			return rep, err
		}
		have[name] = true
		rep.TeamsCreated++
	}
	if err := adoptPool(projectDir, userDir, dst, &rep); err != nil {
		return rep, err
	}
	if rep.TeamsCreated > 0 {
		if err := adoptBoard(projectDir, userDir); err == nil {
			rep.BoardAdopted = true
		}
	}
	if rep.TeamsCreated == 0 && rep.AgentUsersCreated == 0 {
		rep.Skipped = "nothing new to adopt"
		return rep, nil
	}
	if err := markerAdd(userDir, projectDir); err != nil {
		return rep, err
	}
	return rep, nil
}

// adoptPool merges the source agent-user pool into the target by UserID,
// leaving entries the target already has untouched.
func adoptPool(srcDir, dstDir string, dst *TeamStore, rep *AdoptReport) error {
	src, err := loadPoolDocAt(srcDir)
	if err != nil {
		return err
	}
	existing, err := loadPoolDocAt(dstDir)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(existing.AgentUsers))
	for _, u := range existing.AgentUsers {
		seen[u.UserID] = true
	}
	for _, u := range src.AgentUsers {
		if seen[u.UserID] {
			continue
		}
		if err := dst.AddAgentUser(u); err != nil {
			return err
		}
		seen[u.UserID] = true
		rep.AgentUsersCreated++
	}
	return nil
}

// adoptBoard copies the project board into the user store when the user store
// has none; a source without a board, or a target that already has one, is a
// no-op. Best-effort: a copy failure just leaves the global board to start
// fresh, never failing the adoption.
func adoptBoard(srcDir, dstDir string) error {
	src := filepath.Join(srcDir, boardDBFile)
	if _, err := os.Stat(src); err != nil {
		return err
	}
	dst := filepath.Join(dstDir, boardDBFile)
	if _, err := os.Stat(dst); err == nil {
		return os.ErrExist
	}
	return copyFile(src, dst)
}

// adoptTeamHistory carries one team's session selection and member context
// from the legacy project into the user store. Reports whether anything moved;
// a team that never ran a session has no history to carry.
func adoptTeamHistory(srcDir, dstDir, teamName string) (bool, error) {
	adopted := false
	srcSess := filepath.Join(srcDir, sessionDir, teamName+".json")
	if st, err := os.Stat(srcSess); err == nil && !st.IsDir() {
		if err := copyFile(srcSess, filepath.Join(dstDir, sessionDir, teamName+".json")); err != nil {
			return false, err
		}
		adopted = true
	}
	srcCtx := filepath.Join(srcDir, contextRootDir, teamName)
	if st, err := os.Stat(srcCtx); err == nil && st.IsDir() {
		if err := copyDir(srcCtx, filepath.Join(dstDir, contextRootDir, teamName)); err != nil {
			return false, err
		}
		adopted = true
	}
	return adopted, nil
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(sp, dp); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(sp, dp); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, cerr := io.Copy(out, in); cerr != nil {
		out.Close()
		return cerr
	}
	return out.Close()
}

// loadTeamDocAt reads a dir's registry, treating a missing one as empty. The
// legacy teams.json fallback is honoured, mirroring TeamStore.Load.
func loadTeamDocAt(dir string) (TeamDoc, error) {
	empty := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}}
	s, err := NewTeamStoreAt("", dir)
	if err != nil {
		return TeamDoc{}, err
	}
	doc, _, err := s.Load()
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return TeamDoc{}, err
	}
	return doc, nil
}

func loadPoolDocAt(dir string) (AgentUsersDoc, error) {
	empty := AgentUsersDoc{Document: Document{SchemaVersion: SchemaVersion}}
	fs, err := NewFileStore(dir)
	if err != nil {
		return AgentUsersDoc{}, err
	}
	var doc AgentUsersDoc
	if err := fs.Load(AgentUsersFile, &doc); err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return AgentUsersDoc{}, err
	}
	return doc, nil
}

func sameDir(a, b string) bool {
	aa, aerr := filepath.Abs(a)
	bb, berr := filepath.Abs(b)
	if aerr != nil {
		aa = a
	}
	if berr != nil {
		bb = b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func markerHas(userDir, source string) bool {
	var m adoptedMarker
	if err := readMarker(userDir, &m); err != nil {
		return false
	}
	key := dirKey(source)
	for _, s := range m.Sources {
		if s == key {
			return true
		}
	}
	return false
}

func markerAdd(userDir, source string) error {
	var m adoptedMarker
	_ = readMarker(userDir, &m) // a missing marker is an empty list
	m.Document = Document{SchemaVersion: SchemaVersion}
	m.Sources = append(m.Sources, dirKey(source))
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return AtomicWrite(userDir, adoptedMarkerFile, data)
}

func readMarker(userDir string, m *adoptedMarker) error {
	full, err := safePath(userDir, adoptedMarkerFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, m)
}

func dirKey(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return filepath.Clean(a)
	}
	return filepath.Clean(p)
}
