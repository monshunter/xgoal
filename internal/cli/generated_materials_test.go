package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/validationplan"
)

func TestRealCLIAcceptanceMaterialsAndApprovalSurviveRestart(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-materials-cli-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("retained", base)
		} else {
			_ = os.RemoveAll(base)
		}
	})
	binary := compileCurrentDirectoryCLI(t, base)
	for _, name := range []string{"approved", "tampered"} {
		t.Run(name, func(t *testing.T) {
			root, bin := filepath.Join(base, name), filepath.Join(base, name+"-bin")
			cliRepository(t, root)
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			roles := filepath.Join(base, name+".roles")
			currentDirectoryProviderProposal(t, bin, roles, func(p map[string]any) {
				p["contract"].(map[string]any)["generated_validators"] = []any{map[string]any{"id": "output-check", "description": "run the supplied exact output assertion", "runtime": "sh", "script": "sh acceptance.sh", "timeout_seconds": 5}}
			})
			if name == "tampered" {
				file := filepath.Join(bin, "codex")
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				marker := "printf 'accepted\\n' >> output.txt"
				if !strings.Contains(string(data), marker) {
					t.Fatal("missing implementer fixture marker")
				}
				writeCurrentDirectoryFixture(t, file, strings.Replace(string(data), marker, "printf 'true\\n' > acceptance.sh\n"+marker, 1), 0700)
			}
			var env []string
			for _, e := range project.GitEnvironment() {
				if !strings.HasPrefix(e, "PATH=") && !strings.HasPrefix(e, "XGOAL_") {
					env = append(env, e)
				}
			}
			env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XGOAL_RUNTIME_DIR="+filepath.Join(base, "runtime"))
			invoke := func(args ...string) string {
				t.Helper()
				out, err := invokeCurrentDirectoryCLI(binary, env, append([]string{"--project", root}, args...)...)
				if err != nil {
					t.Fatalf("CLI %v: %v\n%s", args, err, out)
				}
				return out
			}
			invoke("init")
			materials := map[string]string{"acceptance.md": "# Acceptance\noutput.txt must contain exactly one accepted line.\n", "acceptance.sh": "test \"$(cat output.txt)\" = accepted\n"}
			for path, data := range materials {
				writeCurrentDirectoryFixture(t, filepath.Join(root, path), data, 0600)
			}
			configPath := filepath.Join(root, "xgoal.yaml")
			configuration, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			configuration = append(configuration, []byte("\nplanning:\n  acceptanceFiles: [acceptance.md]\n  generatedValidators: human-gate\n")...)
			writeCurrentDirectoryFixture(t, configPath, string(configuration), 0600)
			currentDirectoryGit(t, root, "add", "xgoal.yaml", "acceptance.md", "acceptance.sh")
			currentDirectoryGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-qm", "user acceptance inputs")
			head := currentDirectoryGit(t, root, "rev-parse", "HEAD")
			indexBefore, err := os.ReadFile(filepath.Join(root, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			invoke("daemon", "start", "--timeout", "20s")
			t.Cleanup(func() {
				_, _ = invokeCurrentDirectoryCLI(binary, env, "--project", root, "daemon", "stop", "--timeout", "15s")
			})
			goalID := "goal_materials_" + name
			invoke("run", "--id", goalID, "--goal", "write the accepted line", "--acceptance-file", "acceptance.sh")
			stateDir := filepath.Join(root, ".xgoal")
			dsn := (&url.URL{Scheme: "file", Path: filepath.Join(stateDir, "state.db")}).String() + "?mode=ro"
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var gateID string
			awaitBackgroundCondition(t, 30*time.Second, "prepared acceptance approval", func() bool {
				return db.QueryRow(`SELECT id FROM gates WHERE goal_id=? AND state='OPEN' AND reason_code='generated_validation_approval'`, goalID).Scan(&gateID) == nil
			})
			var prepared struct {
				domain.Gate
				Proposal planner.Proposal `json:"prepared_proposal"`
				Hash     string           `json:"prepared_plan_hash"`
			}
			if err := json.Unmarshal([]byte(invoke("gate", "get", gateID)), &prepared); err != nil || prepared.Hash == "" || len(prepared.Proposal.Contract.AcceptanceInputs) != 2 {
				t.Fatalf("approval lacks exact mixed inputs: %+v %v", prepared, err)
			}
			invoke("daemon", "stop", "--timeout", "15s")
			invoke("daemon", "start", "--timeout", "20s")
			var ownerVersion int64
			if err := db.QueryRow(`SELECT version FROM goals WHERE id=?`, goalID).Scan(&ownerVersion); err != nil {
				t.Fatal(err)
			}
			invoke("approve", gateID, "--version", strconv.FormatInt(prepared.Version, 10), "--reason", "reviewed exact prepared acceptance", "--resume", "--owner-version", strconv.FormatInt(ownerVersion, 10))
			var state, finalSet string
			awaitBackgroundCondition(t, 60*time.Second, "material-aware Goal outcome", func() bool {
				err := db.QueryRow(`SELECT state,COALESCE(final_evidence_set_id,'') FROM goals WHERE id=?`, goalID).Scan(&state, &finalSet)
				return err == nil && (state == "COMPLETED" || state == "WAITING")
			})
			if name == "tampered" {
				if state != "WAITING" || finalSet != "" {
					t.Fatalf("modified material completed: %s %s", state, finalSet)
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM gates WHERE goal_id=? AND state='OPEN' AND reason_code='trusted_validator_change'`, goalID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("material trust violation was not identified: %d %v", count, err)
				}
				changed, err := os.ReadFile(filepath.Join(root, "acceptance.sh"))
				if err != nil || string(changed) != "true\n" {
					t.Fatalf("tamper fixture did not reach the actual checkout: %s %v", changed, err)
				}
			} else if state != "COMPLETED" || finalSet == "" {
				t.Fatalf("approved material stalled: %s %s", state, finalSet)
			}
			var raw []byte
			if err := db.QueryRow(`SELECT contract_json FROM goal_revisions WHERE goal_id=?`, goalID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			checkContract := func(raw []byte) {
				t.Helper()
				var envelope struct {
					Contract struct {
						Inputs []validationplan.Input `json:"acceptance_inputs"`
					} `json:"contract"`
				}
				if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Contract.Inputs) != 2 {
					t.Fatalf("lost frozen mixed inputs: %s %v", raw, err)
				}
				for _, i := range envelope.Contract.Inputs {
					if i.Validate() != nil || materials[i.Path] != i.Content {
						t.Fatalf("altered frozen input: %+v", i)
					}
				}
			}
			checkContract(raw)
			rows, err := db.Query(`SELECT role,input_json FROM invocations WHERE goal_id=?`, goalID)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]int{}
			for rows.Next() {
				var role string
				var input []byte
				if err := rows.Scan(&role, &input); err != nil {
					t.Fatal(err)
				}
				var info struct {
					PacketPath string `json:"packet_path"`
				}
				if err := json.Unmarshal(input, &info); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(stateDir, info.PacketPath))
				if err != nil {
					t.Fatal(err)
				}
				switch role {
				case "implementer":
					var p protocol.WorkPacket
					if err := json.Unmarshal(data, &p); err != nil {
						t.Fatal(err)
					}
					checkContract(p.Goal.Contract)
				case "reviewer":
					var p protocol.ReviewPacket
					if err := json.Unmarshal(data, &p); err != nil {
						t.Fatal(err)
					}
					checkContract(p.GoalContract)
				}
				seen[role]++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if seen["planner"] != 1 || seen["implementer"] != 1 || (name == "approved" && seen["reviewer"] != 1) {
				t.Fatalf("approval replayed Provider or lost a packet: %+v", seen)
			}
			if name == "approved" {
				var response struct {
					Report report.Report `json:"json"`
				}
				if err := json.Unmarshal([]byte(invoke("report", goalID)), &response); err != nil {
					t.Fatal(err)
				}
				assertCurrentDirectoryEvidence(t, stateDir, []report.Report{response.Report})
				for path, want := range materials {
					data, err := os.ReadFile(filepath.Join(root, path))
					if err != nil || string(data) != want {
						t.Fatalf("material changed: %s %v", path, err)
					}
				}
			}
			configAfter, _ := os.ReadFile(configPath)
			indexAfter, _ := os.ReadFile(filepath.Join(root, ".git/index"))
			if !bytes.Equal(configuration, configAfter) || !bytes.Equal(indexBefore, indexAfter) || head != currentDirectoryGit(t, root, "rev-parse", "HEAD") {
				t.Fatal("Goal modified user config or Git metadata")
			}
		})
	}
}
