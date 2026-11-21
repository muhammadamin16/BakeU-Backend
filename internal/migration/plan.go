package migration

import (
	"bakeu/internal/identity"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type LegacyUser struct {
	SourceID          string `json:"sourceId"`
	Email             string `json:"email"`
	Phone             string `json:"phone"`
	Role              string `json:"role"`
	FirstName         string `json:"firstName"`
	LastName          string `json:"lastName"`
	ShopName          string `json:"shopName"`
	Address           string `json:"address"`
	PasswordAlgorithm string `json:"passwordAlgorithm"`
	ProfileValid      bool   `json:"profileValid"`
}
type LegacyApplication struct {
	SourceID string `json:"sourceId"`
	UserID   string `json:"userId"`
	ShopName string `json:"shopName"`
	Address  string `json:"address"`
	Status   string `json:"status"`
}
type SQLiteInventory struct {
	SchemaVersion        int                 `json:"schemaVersion"`
	Namespace            string              `json:"namespace"`
	SQLiteSHA256         string              `json:"sqliteSha256"`
	IntegrityCheck       string              `json:"integrityCheck"`
	ForeignKeyViolations int                 `json:"foreignKeyViolations"`
	Users                []LegacyUser        `json:"users"`
	Applications         []LegacyApplication `json:"applications"`
}
type Entry struct {
	Key                string   `json:"key"`
	SourceID           string   `json:"sourceId"`
	Action             string   `json:"action"`
	ProposedRole       string   `json:"proposedRole"`
	TargetCandidates   []string `json:"targetCandidates"`
	Reasons            []string `json:"reasons"`
	RequireLegacyProof bool     `json:"requiresLegacyAccountProof"`
	RequirePhoneProof  bool     `json:"requiresPhoneReverification"`
	RequireRealEmail   bool     `json:"requiresRealEmail"`
	ShopAction         string   `json:"shopAction"`
}
type Summary struct {
	SQLiteForeignKeyViolations int `json:"sqliteForeignKeyViolations"`
	SourceUsers                int `json:"sourceUsers"`
	MongoUsers                 int `json:"mongoUsers"`
	Create                     int `json:"createVerifiedAccounts"`
	Skip                       int `json:"skipAlreadyMapped"`
	Merge                      int `json:"automaticMerges"`
	Conflict                   int `json:"conflictReview"`
	RequireVerification        int `json:"requireVerification"`
	Manual                     int `json:"manualReview"`
	SQLiteOrphanApplications   int `json:"sqliteOrphanApplications"`
	MongoOrphanReferences      int `json:"mongoOrphanReferences"`
	UnsupportedMongoIDs        int `json:"unsupportedMongoIDs"`
}
type Plan struct {
	SchemaVersion int     `json:"schemaVersion"`
	DryRun        bool    `json:"dryRun"`
	Namespace     string  `json:"namespace"`
	SourceSHA256  string  `json:"sourceSha256"`
	Hash          string  `json:"planHash"`
	Summary       Summary `json:"summary"`
	Entries       []Entry `json:"entries"`
}

func Hash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func normalized(raw string) string {
	value, err := identity.NormalizePhone(raw)
	if err != nil {
		return ""
	}
	return value
}
func email(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

func BuildPlan(source SQLiteInventory, target MongoInventory) (Plan, error) {
	out := Plan{SchemaVersion: 1, DryRun: true, Namespace: source.Namespace, SourceSHA256: source.SQLiteSHA256, Entries: []Entry{}, Summary: Summary{SourceUsers: len(source.Users), MongoUsers: len(target.Users), SQLiteForeignKeyViolations: source.ForeignKeyViolations}}
	if source.SchemaVersion != 1 || target.SchemaVersion != 1 || source.Namespace == "" || len(source.Namespace) > 80 || len(source.SQLiteSHA256) != 64 || source.IntegrityCheck != "ok" {
		return out, errors.New("unsupported or incomplete snapshot inventory")
	}
	seenIDs, seenPhones, seenEmails := map[string]int{}, map[string]int{}, map[string]int{}
	for _, u := range source.Users {
		seenIDs[u.SourceID]++
		if p := normalized(u.Phone); p != "" {
			seenPhones[p]++
		}
		if e := email(u.Email); e != "" && !strings.HasSuffix(e, "@accounts.bakeu.invalid") {
			seenEmails[e]++
		}
	}
	users := map[string]MongoUser{}
	for _, u := range target.Users {
		users[u.ID] = u
		if u.IDType != "string" {
			out.Summary.UnsupportedMongoIDs++
		}
	}
	mappings := map[string][]string{}
	for _, m := range target.Mappings {
		if m.Namespace == source.Namespace {
			mappings[m.SourceID] = append(mappings[m.SourceID], m.TargetUID)
		}
	}
	for _, a := range source.Applications {
		if seenIDs[a.UserID] == 0 {
			out.Summary.SQLiteOrphanApplications++
		}
	}
	for _, r := range target.References {
		if _, exists := users[r.UserID]; !exists {
			out.Summary.MongoOrphanReferences++
		}
	}
	ordered := append([]LegacyUser(nil), source.Users...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SourceID < ordered[j].SourceID })
	for _, u := range ordered {
		entry := Entry{Key: Hash([]string{source.Namespace, u.SourceID}), SourceID: u.SourceID, Action: "hold_for_claim", RequireLegacyProof: true, RequirePhoneProof: true, TargetCandidates: []string{}, Reasons: []string{}, ShopAction: "none"}
		switch u.Role {
		case "client":
			entry.ProposedRole = "customer"
		case "seller":
			entry.ProposedRole = "seller"
			entry.ShopAction = "hold_draft_until_owner_proof"
		default:
			entry.Action = "manual_review"
			entry.Reasons = append(entry.Reasons, "unsupported_source_role")
		}
		phone := normalized(u.Phone)
		em := email(u.Email)
		entry.RequireRealEmail = em == "" || strings.HasSuffix(em, "@accounts.bakeu.invalid") || !strings.Contains(em, "@")
		if u.SourceID == "" || seenIDs[u.SourceID] > 1 {
			entry.Action = "manual_review"
			entry.Reasons = append(entry.Reasons, "invalid_or_duplicate_source_id")
		}
		if phone == "" {
			entry.Action = "manual_review"
			entry.Reasons = append(entry.Reasons, "invalid_contact_requires_reverification")
		}
		if !u.ProfileValid {
			entry.Action = "manual_review"
			entry.Reasons = append(entry.Reasons, "invalid_source_profile")
		}
		if u.PasswordAlgorithm != "node-scrypt" {
			entry.Action = "manual_review"
			entry.Reasons = append(entry.Reasons, "legacy_password_proof_unavailable")
		}
		if phone != "" && seenPhones[phone] > 1 || !entry.RequireRealEmail && seenEmails[em] > 1 {
			entry.Action = "conflict_review"
			entry.Reasons = append(entry.Reasons, "duplicate_source_contact")
		}
		for _, candidate := range target.Users {
			phoneMatch := phone != "" && (phone == normalized(candidate.Phone) || phone == normalized(candidate.VerifiedPhone))
			emailMatch := !entry.RequireRealEmail && em == email(candidate.Email)
			if phoneMatch || emailMatch {
				entry.TargetCandidates = append(entry.TargetCandidates, candidate.ID)
			}
		}
		sort.Strings(entry.TargetCandidates)
		if len(entry.TargetCandidates) > 0 {
			entry.Action = "conflict_review"
			entry.Reasons = append(entry.Reasons, "candidate_match_is_not_ownership_proof")
		}
		if mapped := mappings[u.SourceID]; len(mapped) > 0 {
			if len(mapped) == 1 {
				if targetUser, exists := users[mapped[0]]; exists && targetUser.IDType == "string" {
					entry.Action = "skip_mapped"
					entry.TargetCandidates = []string{mapped[0]}
					entry.RequireLegacyProof = false
					entry.RequirePhoneProof = false
					entry.Reasons = append(entry.Reasons, "existing_explicit_identity_mapping")
				} else {
					entry.Action = "conflict_review"
					entry.Reasons = append(entry.Reasons, "orphan_identity_mapping")
				}
			} else {
				entry.Action = "conflict_review"
				entry.Reasons = append(entry.Reasons, "duplicate_identity_mapping")
			}
		}
		if entry.Action != "skip_mapped" {
			entry.Reasons = append(entry.Reasons, "no_retained_legacy_ownership_proof", "password_not_copied_use_reverification")
		}
		switch entry.Action {
		case "skip_mapped":
			out.Summary.Skip++
		case "conflict_review":
			out.Summary.Conflict++
		case "manual_review":
			out.Summary.Manual++
		default:
			out.Summary.RequireVerification++
		}
		out.Entries = append(out.Entries, entry)
	}
	out.Hash = out.Digest()
	return out, nil
}

func (p Plan) Digest() string {
	return Hash(struct {
		Namespace string
		Source    string
		Entries   []Entry
		Summary   Summary
	}{p.Namespace, p.SourceSHA256, p.Entries, p.Summary})
}
