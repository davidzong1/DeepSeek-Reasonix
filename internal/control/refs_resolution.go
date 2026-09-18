package control

import (
	"context"
	"html"
	"strings"
)

// ResolveRefs resolves the @references in a line into a single tagged context
// block (file/dir contents, MCP resource bodies), plus per-reference errors.
func (c *Controller) ResolveRefs(ctx context.Context, line string) (block string, errs []string) {
	resolved := c.resolveRefsForTurn(ctx, line, false)
	return resolved.block, resolved.errs
}

// ResolveScopedRefs is the HTTP/frontend variant: file references are honored
// only when they can be resolved under the controller workspace root.
func (c *Controller) ResolveScopedRefs(ctx context.Context, line string) (block string, errs []string) {
	resolved := c.resolveRefsForTurn(ctx, line, true)
	return resolved.block, resolved.errs
}

type resolvedReferences struct {
	block  string
	errs   []string
	images []string
}

func (c *Controller) resolveUnscopedRefsForTurn(ctx context.Context, line string) resolvedReferences {
	return c.resolveRefsForTurn(ctx, line, false)
}

func (c *Controller) resolveScopedRefsForTurn(ctx context.Context, line string) resolvedReferences {
	return c.resolveRefsForTurn(ctx, line, true)
}

func (c *Controller) resolveRefsForTurn(ctx context.Context, line string, scopedOnly bool) resolvedReferences {
	refs := resolveBareNames(c.detectRefsMode(line, scopedOnly), c.workspaceRoot)
	var b strings.Builder
	var errs, images []string
	seenImages := map[string]bool{}
	addImage := func(r ref) (string, bool) {
		value, err := c.resolveReferenceImage(r)
		if err != nil {
			errs = append(errs, "@"+r.raw+" — "+err.Error())
			return "", false
		}
		if value == "" {
			errs = append(errs, "@"+r.raw+" — image reference resolved to an empty input")
			return "", false
		}
		if !seenImages[value] {
			seenImages[value] = true
			images = append(images, value)
		}
		return value, true
	}
	includedInstructionPaths := map[string]bool{}
	includedInstructionBodies := map[string]bool{}
	if current := c.memory.current(); current != nil {
		for _, doc := range current.Docs {
			includedInstructionPaths[cleanAbsPath(doc.Path)] = true
			includedInstructionBodies[doc.Body] = true
		}
	}
	for _, r := range refs {
		switch r.kind {
		case refResource:
			text, err := c.mcp.readResource(ctx, r.server, r.uri)
			if err != nil {
				errs = append(errs, "@"+r.raw+" — "+err.Error())
				continue
			}
			appendRefBlock(&b, "resource", `ref="@`+r.raw+`"`, text)
		case refFile:
			baseDir := c.workspaceRoot
			if r.baseDir != "" {
				baseDir = r.baseDir
			}
			attached := false
			if isImageAttachmentRef(r.path) {
				_, attached = addImage(r)
			}
			text, isDir, err := readFileRefWithVision(r.path, baseDir, attached && c.imageInputEnabled())
			if err != nil {
				errs = append(errs, "@"+r.raw+" — "+err.Error())
				continue
			}
			pathInstructions, diagnostics := c.resolveReferencedInstructions(r, baseDir, includedInstructionPaths, includedInstructionBodies)
			if pathInstructions != "" {
				appendRefBlock(&b, "path-instructions", `target="`+html.EscapeString(displayPathForRef(r))+`"`, pathInstructions)
			}
			for _, diagnostic := range diagnostics {
				errs = append(errs, "@"+r.raw+" — "+diagnostic.Message)
			}
			tag := "file"
			if isDir {
				tag = "dir"
			}
			displayPath := r.path
			if r.displayPath != "" {
				displayPath = r.displayPath
			}
			appendRefBlock(&b, tag, `path="`+displayPath+`"`, text)
		case refImage, refRemoteImage, refFileID:
			if _, attached := addImage(r); attached {
				appendRefBlock(&b, "image", `path="`+r.path+`"`, imageAttachmentNote(r.path, c.imageInputEnabled()))
			}
		}
	}
	for _, r := range bareVisionRefs(line) {
		addImage(r)
	}
	return resolvedReferences{block: b.String(), errs: errs, images: images}
}
