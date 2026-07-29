package main

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/pelletier/go-toml/v2"
)

type Lock struct {
	entries map[agentskill.SkillID]LockedSkill
}

func NewLock(skills []LockedSkill) (Lock, error) {
	lock := Lock{entries: make(map[agentskill.SkillID]LockedSkill, len(skills))}
	names := make(map[string]agentskill.SkillID, len(skills))
	for _, skill := range skills {
		publication := skill.Publication
		identity := publication.Skill()
		if identity.String() == "" {
			return Lock{}, fmt.Errorf("lock contains an invalid skill")
		}
		if _, exists := lock.entries[identity]; exists {
			return Lock{}, fmt.Errorf("lock contains duplicate skill %s", identity.String())
		}
		name := identity.Name().String()
		if prior, exists := names[name]; exists {
			return Lock{}, fmt.Errorf("skills %s and %s map to the same destination", prior.String(), identity.String())
		}
		lock.entries[identity] = skill
		names[name] = identity
	}
	return lock, nil
}

func (l Lock) Skills() []LockedSkill {
	skills := make([]LockedSkill, 0, len(l.entries))
	for _, skill := range l.entries {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Publication.Skill().String() < skills[j].Publication.Skill().String()
	})
	return skills
}

func (l Lock) Lookup(skill agentskill.SkillID) (LockedSkill, bool) {
	locked, found := l.entries[skill]
	return locked, found
}

func (l Lock) With(skill LockedSkill) (Lock, error) {
	all := l.Skills()
	identity := skill.Publication.Skill()
	replaced := false
	for index := range all {
		if all[index].Publication.Skill() == identity {
			all[index] = skill
			replaced = true
			break
		}
	}
	if !replaced {
		all = append(all, skill)
	}
	return NewLock(all)
}

type lockDocument struct {
	Schema *int        `toml:"schema"`
	Skills []lockEntry `toml:"skills"`
}

type lockEntry struct {
	Skill  string `toml:"skill"`
	Digest string `toml:"digest"`
}

func DecodeLock(source io.Reader) (Lock, error) {
	if source == nil {
		return Lock{}, fmt.Errorf("decode project lock: reader is nil")
	}
	var document lockDocument
	decoder := toml.NewDecoder(source)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Lock{}, fmt.Errorf("decode project lock: %w", err)
	}
	if document.Schema == nil {
		return Lock{}, fmt.Errorf("decode project lock: schema is required")
	}
	if *document.Schema != 1 {
		return Lock{}, fmt.Errorf("decode project lock: unsupported schema %d", *document.Schema)
	}

	skills := make([]LockedSkill, 0, len(document.Skills))
	previousSkill := ""
	for index, entry := range document.Skills {
		identity, err := agentskill.ParseSkillID(entry.Skill)
		if err != nil {
			return Lock{}, fmt.Errorf("decode project lock skill %d: %w", index+1, err)
		}
		canonicalSkill := identity.String()
		if index > 0 && canonicalSkill <= previousSkill {
			return Lock{}, fmt.Errorf("decode project lock: skills must be sorted by canonical identity")
		}
		previousSkill = canonicalSkill
		digest, err := agentskill.ParseTreeDigest(entry.Digest)
		if err != nil {
			return Lock{}, fmt.Errorf("decode project lock skill %s: %w", entry.Skill, err)
		}
		publication, err := agentskill.NewPublicationID(identity, digest)
		if err != nil {
			return Lock{}, fmt.Errorf("decode project lock skill %s: %w", entry.Skill, err)
		}
		skills = append(skills, LockedSkill{Publication: publication})
	}
	lock, err := NewLock(skills)
	if err != nil {
		return Lock{}, fmt.Errorf("decode project lock: %w", err)
	}
	return lock, nil
}

func EncodeLock(destination io.Writer, lock Lock) error {
	if destination == nil {
		return fmt.Errorf("encode project lock: writer is nil")
	}
	buffer := bufio.NewWriter(destination)
	if _, err := buffer.WriteString("schema = 1\n"); err != nil {
		return fmt.Errorf("encode project lock: %w", err)
	}
	for _, locked := range lock.Skills() {
		publication := locked.Publication
		if _, err := fmt.Fprintf(buffer, "\n[[skills]]\nskill = %s\ndigest = %s\n", strconv.Quote(publication.Skill().String()), strconv.Quote(publication.Tree().String())); err != nil {
			return fmt.Errorf("encode project lock: %w", err)
		}
	}
	if err := buffer.Flush(); err != nil {
		return fmt.Errorf("encode project lock: %w", err)
	}
	return nil
}
