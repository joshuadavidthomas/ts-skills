package client

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

type lock struct {
	entries map[registry.SkillID]lockedSkill
}

func newLock(skills []lockedSkill) (lock, error) {
	result := lock{entries: make(map[registry.SkillID]lockedSkill, len(skills))}
	names := make(map[string]registry.SkillID, len(skills))
	for _, skill := range skills {
		publication := skill.publication
		identity := publication.Skill()
		if identity.String() == "" {
			return lock{}, fmt.Errorf("lock contains an invalid skill")
		}
		if _, exists := result.entries[identity]; exists {
			return lock{}, fmt.Errorf("lock contains duplicate skill %s", identity.String())
		}
		name := identity.Name().String()
		if prior, exists := names[name]; exists {
			return lock{}, fmt.Errorf("skills %s and %s map to the same destination", prior.String(), identity.String())
		}
		result.entries[identity] = skill
		names[name] = identity
	}
	return result, nil
}

func (l lock) skills() []lockedSkill {
	skills := make([]lockedSkill, 0, len(l.entries))
	for _, skill := range l.entries {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].publication.Skill().String() < skills[j].publication.Skill().String()
	})
	return skills
}

func (l lock) lookup(skill registry.SkillID) (lockedSkill, bool) {
	locked, found := l.entries[skill]
	return locked, found
}

func (l lock) with(skill lockedSkill) (lock, error) {
	all := l.skills()
	identity := skill.publication.Skill()
	replaced := false
	for index := range all {
		if all[index].publication.Skill() == identity {
			all[index] = skill
			replaced = true
			break
		}
	}
	if !replaced {
		all = append(all, skill)
	}
	return newLock(all)
}

type lockDocument struct {
	Schema *int        `toml:"schema"`
	Skills []lockEntry `toml:"skills"`
}

type lockEntry struct {
	Skill  string `toml:"skill"`
	Digest string `toml:"digest"`
}

func decodeLock(source io.Reader) (lock, error) {
	if source == nil {
		return lock{}, fmt.Errorf("decode project lock: reader is nil")
	}
	var document lockDocument
	metadata, err := toml.NewDecoder(source).Decode(&document)
	if err != nil {
		return lock{}, fmt.Errorf("decode project lock: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return lock{}, fmt.Errorf("decode project lock: unknown field %q", undecoded[0].String())
	}
	if document.Schema == nil {
		return lock{}, fmt.Errorf("decode project lock: schema is required")
	}
	if *document.Schema != 1 {
		return lock{}, fmt.Errorf("decode project lock: unsupported schema %d", *document.Schema)
	}

	skills := make([]lockedSkill, 0, len(document.Skills))
	previousSkill := ""
	for index, entry := range document.Skills {
		identity, err := registry.ParseSkillID(entry.Skill)
		if err != nil {
			return lock{}, fmt.Errorf("decode project lock skill %d: %w", index+1, err)
		}
		canonicalSkill := identity.String()
		if index > 0 && canonicalSkill <= previousSkill {
			return lock{}, fmt.Errorf("decode project lock: skills must be sorted by canonical identity")
		}
		previousSkill = canonicalSkill
		digest, err := registry.ParseTreeDigest(entry.Digest)
		if err != nil {
			return lock{}, fmt.Errorf("decode project lock skill %s: %w", entry.Skill, err)
		}
		publication, err := registry.NewPublicationID(identity, digest)
		if err != nil {
			return lock{}, fmt.Errorf("decode project lock skill %s: %w", entry.Skill, err)
		}
		skills = append(skills, lockedSkill{publication: publication})
	}
	decoded, err := newLock(skills)
	if err != nil {
		return lock{}, fmt.Errorf("decode project lock: %w", err)
	}
	return decoded, nil
}

func encodeLock(destination io.Writer, lock lock) error {
	if destination == nil {
		return fmt.Errorf("encode project lock: writer is nil")
	}
	buffer := bufio.NewWriter(destination)
	if _, err := buffer.WriteString("schema = 1\n"); err != nil {
		return fmt.Errorf("encode project lock: %w", err)
	}
	for _, locked := range lock.skills() {
		publication := locked.publication
		if _, err := fmt.Fprintf(buffer, "\n[[skills]]\nskill = %s\ndigest = %s\n", strconv.Quote(publication.Skill().String()), strconv.Quote(publication.Tree().String())); err != nil {
			return fmt.Errorf("encode project lock: %w", err)
		}
	}
	if err := buffer.Flush(); err != nil {
		return fmt.Errorf("encode project lock: %w", err)
	}
	return nil
}
