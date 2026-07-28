package install

import (
	"fmt"
	"sort"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
)

type Lock struct {
	entries map[registry.SkillID]LockedSkill
}

func NewLock(skills []LockedSkill) (Lock, error) {
	lock := Lock{entries: make(map[registry.SkillID]LockedSkill, len(skills))}
	names := make(map[string]registry.SkillID, len(skills))
	for _, skill := range skills {
		publication := skill.Publication()
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
		return skills[i].Publication().Skill().String() < skills[j].Publication().Skill().String()
	})
	return skills
}

func (l Lock) Lookup(skill registry.SkillID) (LockedSkill, bool) {
	locked, found := l.entries[skill]
	return locked, found
}

func (l Lock) With(skill LockedSkill) (Lock, error) {
	all := l.Skills()
	identity := skill.Publication().Skill()
	replaced := false
	for index := range all {
		if all[index].Publication().Skill() == identity {
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
