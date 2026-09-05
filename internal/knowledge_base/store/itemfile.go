package store

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"reasonix/internal/frontmatter"
	"reasonix/internal/knowledge_base/model"
)

// itemFile mirrors KnowledgeItem field-for-field so the YAML key order is
// fixed and unknown keys are rejected on read (KnownFields).
type itemFile struct {
	ID           string         `yaml:"id"`
	Canonical    string         `yaml:"canonical"`
	AuthorID     string         `yaml:"author_id"`
	Title        string         `yaml:"title"`
	Kind         model.ItemKind `yaml:"kind"`
	Scope        model.Scope    `yaml:"scope"`
	Tags         []string       `yaml:"tags,omitempty"`
	Provenance   []model.Ref    `yaml:"provenance"`
	Quality      qualityFile    `yaml:"quality"`
	Version      int            `yaml:"version"`
	Status       model.Status   `yaml:"status"`
	CreatedAt    time.Time      `yaml:"created_at"`
	UpdatedAt    time.Time      `yaml:"updated_at"`
	Supersedes   string         `yaml:"supersedes,omitempty"`
	SupersededBy string         `yaml:"superseded_by,omitempty"`
	ConflictWith string         `yaml:"conflict_with,omitempty"`
}

type qualityFile struct {
	Confidence  float64           `yaml:"confidence"`
	ReviewLevel model.ReviewLevel `yaml:"review_level,omitempty"`
	Checks      []string          `yaml:"checks,omitempty"`
	Suspect     bool              `yaml:"suspect,omitempty"`
}

// marshalItem serializes an item to its frontmatter+markdown file form.
func marshalItem(i model.KnowledgeItem) ([]byte, error) {
	f := itemFile{
		ID: i.ID, Canonical: i.Canonical, AuthorID: i.AuthorID, Title: i.Title,
		Kind: i.Kind, Scope: i.Scope, Tags: i.Tags, Provenance: i.Provenance,
		Quality: qualityFile{
			Confidence: i.Quality.Confidence, ReviewLevel: i.Quality.ReviewLevel,
			Checks: i.Quality.Checks, Suspect: i.Quality.Suspect,
		},
		Version: i.Version, Status: i.Status,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
		Supersedes: i.Supersedes, SupersededBy: i.SupersededBy, ConflictWith: i.ConflictWith,
	}
	y, err := yaml.Marshal(&f)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(y)
	buf.WriteString("---\n")
	buf.WriteString(i.Body)
	return buf.Bytes(), nil
}

// unmarshalItem parses a frontmatter item file into the model value.
func unmarshalItem(data []byte) (model.KnowledgeItem, error) {
	var f itemFile
	body, err := frontmatter.Decode(string(data), &f, frontmatter.DecodeOptions{KnownFields: true})
	if err != nil {
		return model.KnowledgeItem{}, fmt.Errorf("store: frontmatter: %w", err)
	}
	i := model.KnowledgeItem{
		ID: f.ID, Canonical: f.Canonical, AuthorID: f.AuthorID, Title: f.Title,
		Kind: f.Kind, Scope: f.Scope, Tags: f.Tags, Provenance: f.Provenance,
		Quality: model.QualitySignal{
			Confidence: f.Quality.Confidence, ReviewLevel: f.Quality.ReviewLevel,
			Checks: f.Quality.Checks, Suspect: f.Quality.Suspect,
		},
		Version: f.Version, Status: f.Status,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
		Supersedes: f.Supersedes, SupersededBy: f.SupersededBy, ConflictWith: f.ConflictWith,
		Body: body,
	}
	if i.CreatedAt.IsZero() {
		return model.KnowledgeItem{}, fmt.Errorf("store: item %q missing created_at", i.ID)
	}
	return i, nil
}
