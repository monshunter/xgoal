package exporter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/monshunter/xgoal/internal/acceptance"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/scenario"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workpacket"
	"github.com/monshunter/xgoal/internal/workspace"
)

func decode(data []byte, target any) error {
	if !json.Valid(data) {
		return errors.New("invalid artifact JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	want, err := canonical.Marshal(target)
	if err != nil || !bytes.Equal(data, want) {
		return errors.New("artifact is not canonical")
	}
	return nil
}
func match(got, want string, err error) error {
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("artifact protocol hash or owner binding mismatch")
	}
	return nil
}

func (e *writer) artifact(a sqlite.SnapshotArtifact, m *Manifest) error {
	if !invocation.Component(a.ID) {
		return errors.New("unsafe artifact owner ID")
	}
	root := filepath.Join(e.root, "files")
	switch a.Kind {
	case "work_packet":
		if _, err := e.copy(filepath.Join("packets", a.ID, "packet.json"), "", 8<<20); err != nil {
			return err
		}
		s, err := workpacket.NewStore(root)
		if err != nil {
			return err
		}
		p, err := s.Load(a.ID)
		if err != nil {
			return err
		}
		return match(p.Hash, a.Hash, nil)
	case "patch":
		rel, err := e.relative(a.Path)
		if err != nil || rel != filepath.Join("patches", a.ID) {
			return errors.New("patch path differs from its owner")
		}
		data, err := e.copy(filepath.Join(rel, "manifest.json"), "", 8<<20)
		if err != nil {
			return err
		}
		var p protocol.PatchBundle
		if err := decode(data, &p); err != nil {
			return err
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if p.AttemptID != a.ID || p.ManifestHash != a.Hash || p.BundleHash != a.AuxHash {
			return errors.New("patch owner mismatch")
		}
		for _, object := range p.Objects {
			data, err := e.copy(filepath.Join(rel, object.Ref), object.Hash, maxFileBytes)
			if err != nil {
				return err
			}
			if int64(len(data)) != object.Length {
				return errors.New("patch object size mismatch")
			}
		}
		s, err := patch.NewStore(root)
		if err != nil {
			return err
		}
		_, err = s.Load(a.ID)
		return err
	case "receipt":
		data, err := e.copy(filepath.Join("validator", "receipts", a.ID+".json"), "", 1<<20)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, a.Data) {
			return errors.New("receipt differs from frozen database bytes")
		}
		var r protocol.CommandReceipt
		if err := decode(data, &r); err != nil {
			return err
		}
		hash, err := r.Hash()
		if err := match(hash, a.Hash, err); err != nil {
			return err
		}
		if r.ID != a.ID || r.StdoutRef != "logs/"+a.ID+".stdout.log" || r.StderrRef != "logs/"+a.ID+".stderr.log" {
			return errors.New("receipt log owner mismatch")
		}
		for _, ref := range []string{r.StdoutRef, r.StderrRef} {
			if _, err := e.copy(filepath.Join("validator", ref), "", maxFileBytes); err != nil {
				return err
			}
		}
		_, err = validator.ReadReceipt(root, a.ID)
		return err
	case "review_packet", "review_result":
		rel, err := e.relative(a.Path)
		if err != nil {
			return err
		}
		name := "packet.json"
		if a.Kind == "review_result" {
			name = "result.json"
		}
		if rel != filepath.Join("reviews", a.ID, name) {
			return errors.New("review path owner mismatch")
		}
		if _, err := e.copy(rel, "", 8<<20); err != nil {
			return err
		}
		if a.Kind == "review_result" {
			_, hash, err := review.ReadResult(filepath.Join(root, rel))
			return match(hash, a.Hash, err)
		}
		p, hash, err := review.ReadPacket(filepath.Join(root, rel))
		if err := match(hash, a.Hash, err); err != nil {
			return err
		}
		bundle, ok := e.refs["patch:"+p.ImplementationAttemptID]
		if !ok || p.ID != a.ID || bundle.Path != p.PatchBundlePath || bundle.AuxHash != p.PatchBundleHash {
			return errors.New("review patch reference is not bound in snapshot")
		}
		for _, r := range p.ValidatorReceipts {
			owner, ok := e.refs["receipt:"+r.ID]
			if !ok || owner.Hash != r.Hash || r.Path != filepath.Join(e.source, "validator", "receipts", r.ID+".json") {
				return errors.New("review receipt is not bound in snapshot")
			}
		}
		return nil
	case "scenario":
		dir := filepath.Join("scenarios", a.ID)
		data, err := e.copy(filepath.Join(dir, "manifest.json"), "", 2<<20)
		if err != nil {
			return err
		}
		var manifest scenario.Manifest
		if err := decode(data, &manifest); err != nil {
			return err
		}
		if err := manifest.Validate(); err != nil {
			return err
		}
		if manifest.EvidenceID != a.ID || manifest.Hash != a.Hash {
			return errors.New("scenario owner mismatch")
		}
		for _, f := range manifest.Files {
			data, err := e.copy(filepath.Join(dir, "files", f.Path), f.SHA256, 32<<20)
			if err != nil {
				return err
			}
			if int64(len(data)) != f.Size {
				return errors.New("scenario file size mismatch")
			}
		}
		_, err = scenario.Load(e.ctx, root, a.ID)
		return err
	case "environment":
		var env protocol.EnvironmentSnapshot
		if err := decode(a.Data, &env); err != nil {
			return err
		}
		hash, err := env.Hash()
		return match(hash, a.Hash, err)
	case "report":
		md, ok := e.refs["report_markdown:"+a.ID]
		if !ok {
			return errors.New("report Markdown owner missing")
		}
		artifact := report.Artifact{JSON: a.Data, Markdown: a.AuxData, JSONHash: a.Hash, MarkdownHash: md.Hash, ReportHash: a.AuxHash}
		r, err := report.VerifyArtifact(artifact)
		if err != nil {
			return err
		}
		if r.Goal.ID != a.ID {
			return errors.New("report Goal mismatch")
		}
		for _, s := range r.Scenarios {
			ref, ok := e.refs["scenario:"+s.EvidenceID]
			if !ok || ref.Hash != s.Hash {
				return errors.New("report scenario owner missing")
			}
		}
		return e.reportFile(a)
	case "report_markdown":
		return e.reportFile(a)
	case "workspace":
		if a.State == "CLEANED" {
			e.setStatus(a, "retired")
			return nil
		}
		data, err := e.copy(a.Path, "", 1<<20)
		if err != nil {
			return err
		}
		s, err := workspace.DecodeMarkerSnapshot(a.Path, data)
		if err != nil {
			return err
		}
		if s.ID != a.ID {
			return errors.New("workspace owner mismatch")
		}
		return match(s.MarkerHash, a.Hash, nil)
	case "migration_backup":
		if !invocation.Component(a.Path) {
			return errors.New("unsafe migration backup name")
		}
		_, err := e.copy(filepath.Join("backups", a.Path), a.Hash, 512<<20)
		return err
	case "invocation":
		return e.logs(a, m)
	case "acceptance":
		var r acceptance.Request
		if err := decode(a.Data, &r); err != nil {
			return err
		}
		if err := r.Packet.Validate(); err != nil {
			return err
		}
		data, err := e.copy(r.PacketPath, "", 16<<20)
		if err != nil {
			return err
		}
		want, err := canonical.Marshal(r.Packet)
		if err != nil || !bytes.Equal(data, want) {
			return errors.New("acceptance Packet differs from frozen request")
		}
		hash, err := canonical.Hash("acceptance-packet", acceptance.PacketVersion, r.Packet)
		return match(hash, r.PacketHash, err)
	case "planner":
		var r planner.Request
		if err := decode(a.Data, &r); err != nil {
			return err
		}
		if r.Proposal != nil {
			e.setStatus(a, "provided-proposal; no Provider Packet")
			return nil
		}
		if len(a.AuxData) == 0 {
			e.setStatus(a, "not-started")
			return nil
		}
		var o planner.Observation
		if err := decode(a.AuxData, &o); err != nil {
			return err
		}
		if o.InvocationID == "" {
			e.setStatus(a, "not-started")
			return nil
		}
		if !invocation.Component(r.GoalID) || !invocation.Component(o.InvocationID) || r.Generation < 1 {
			return errors.New("invalid planning owner")
		}
		path := filepath.Join("planner", r.GoalID, fmt.Sprintf("%d-%s", r.Generation, o.InvocationID), "packet.json")
		registered, hasIndex := e.refs["invocation:"+o.InvocationID]
		if hasIndex {
			var in invocation.Input
			if err := decode(registered.Data, &in); err != nil {
				return err
			}
			if in.Role != "planner" || in.OwnerID != a.ID || in.GoalID != r.GoalID || in.Generation != r.Generation || in.RequestHash != a.Hash || in.PacketPath != path || in.InputTree != o.InputTree {
				return errors.New("Planner invocation index differs from Effect")
			}
		}
		data, err := e.copy(path, "", 16<<20)
		if err != nil {
			// BeginPlanning precedes Harness/Probe/Profile preflight and Packet
			// creation. An unindexed preflight failure or crash is not a sealed
			// file reference. A returned legacy Proposal/session proves that the
			// Provider ran, and registered inputs are always mandatory.
			if errors.Is(err, os.ErrNotExist) && !hasIndex && o.Proposal == nil && o.SessionID == "" {
				e.setStatus(a, "unregistered-input-unknown; preflight or legacy failure")
				return nil
			}
			return err
		}
		var p planner.Packet
		if err := decode(data, &p); err != nil {
			return err
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if p.GoalID != r.GoalID || p.RawGoal != r.RawGoal || p.ConfigHash != r.ConfigHash || p.Mode != r.Mode || !slices.Equal(p.TrustedValidators, r.TrustedValidatorIDs) || !reflect.DeepEqual(p.Prior, r.Prior) || !reflect.DeepEqual(p.ValidationCapabilities, r.ValidationCapabilities) {
			return errors.New("planning Packet owner mismatch")
		}
		if !hasIndex {
			e.setStatus(a, "legacy Packet; structure and frozen request fields verified; no frozen byte hash or indexed Provider stream")
		}
		return nil
	default:
		return errors.New("unknown snapshot artifact owner")
	}
}

func (e *writer) setStatus(a sqlite.SnapshotArtifact, status string) {
	if e.artifactStatus == nil {
		e.artifactStatus = map[string]string{}
	}
	e.artifactStatus[a.Kind+":"+a.ID] = status
}

func (e *writer) reportFile(a sqlite.SnapshotArtifact) error {
	if invocation.SHA256(a.Data) != a.Hash {
		return errors.New("report bytes changed")
	}
	if a.State == "COMMITTED" {
		_, err := e.copy(a.Path, a.Hash, 16<<20)
		return err
	}
	if a.State != "PENDING_RENAME" {
		return errors.New("unknown report publication state")
	}
	rel, err := e.relative(a.Path)
	if err != nil {
		return err
	}
	return e.write(filepath.Join("files", rel), a.Data, 0600, "database blob; original report remains PENDING_RENAME", "")
}

func (e *writer) logs(a sqlite.SnapshotArtifact, m *Manifest) error {
	var in invocation.Input
	var obs invocation.Observation
	if err := decode(a.Data, &in); err != nil {
		return err
	}
	if err := decode(a.AuxData, &obs); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	hash, err := canonical.Hash("invocation-input", invocation.Version, in)
	if err := match(hash, a.Hash, err); err != nil {
		return err
	}
	if in.ID != a.ID || obs.Status != a.State || obs.Unavailable != "" {
		return errors.New("invocation identity or durable log index unavailable")
	}
	if _, err := e.copy(in.PacketPath, in.PacketSHA256, 16<<20); err != nil {
		return err
	}
	for _, stream := range []struct {
		name          string
		cursor, bytes int64
	}{{"stdout", obs.Cursor, obs.Bytes}, {"stderr", obs.StderrCursor, obs.StderrBytes}} {
		if stream.cursor < 0 || stream.cursor > invocation.MaxEvents || stream.bytes < 0 {
			return errors.New("invalid invocation durable boundary")
		}
		dir := in.ProviderDir
		if stream.name == "stderr" {
			dir = filepath.Join(dir, "stderr")
		}
		var total int64
		for seq := int64(1); seq <= stream.cursor; seq++ {
			if err := e.ctx.Err(); err != nil {
				return err
			}
			rel := filepath.Join(dir, "events", fmt.Sprintf("%06d.json", seq))
			data, _, err := invocation.ReadFile(e.source, rel, invocation.MaxEventBytes)
			if err != nil {
				return err
			}
			total += int64(len(data))
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				return err
			}
			if typ, _ := raw["type"].(string); typ == "" {
				return errors.New("invocation event type missing")
			}
			public, err := json.Marshal(invocation.Public(raw))
			if err != nil {
				return err
			}
			if err := e.write(filepath.Join("files", rel), public, 0600, "public log projection", invocation.SHA256(data)); err != nil {
				return err
			}
		}
		if total != stream.bytes {
			return errors.New("invocation durable byte boundary differs from snapshot")
		}
	}
	m.Logs = append(m.Logs, LogBoundary{ID: in.ID, Stdout: obs.Cursor, Stderr: obs.StderrCursor, Status: obs.Status})
	return nil
}
