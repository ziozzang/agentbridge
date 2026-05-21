package pii

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

type Pattern struct {
	Name  string `yaml:"name"`
	Regex string `yaml:"regex"`
	Mask  string `yaml:"mask"`
	re    *regexp.Regexp
}

type Detector struct {
	mu       sync.RWMutex
	patterns []Pattern
	maxToken int
}

type Mapping map[string]string

type MaskBuilder struct {
	d        *Detector
	counters map[string]int
	mapping  Mapping
	dedupe   map[string]string
	salt     string
}

func New() *Detector {
	d := &Detector{}
	for _, p := range Defaults() {
		_ = d.Add(p)
	}
	return d
}

func NewEmpty() *Detector { return &Detector{} }

func (d *Detector) Add(p Pattern) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("pii pattern name is required")
	}
	if strings.TrimSpace(p.Regex) == "" {
		return fmt.Errorf("pii pattern %q: regex is required", p.Name)
	}
	re, err := regexp.Compile(p.Regex)
	if err != nil {
		return fmt.Errorf("pii pattern %q: %w", p.Name, err)
	}
	if p.Mask == "" {
		p.Mask = "[MASK_" + strings.ToUpper(p.Name) + "_{n}]"
	}
	if !strings.Contains(p.Mask, "{n}") {
		p.Mask = strings.TrimSuffix(p.Mask, "]") + "_{n}]"
	}
	p.re = re
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, existing := range d.patterns {
		if existing.Name == p.Name {
			d.patterns[i] = p
			d.recomputeMaxLocked()
			return nil
		}
	}
	d.patterns = append(d.patterns, p)
	d.recomputeMaxLocked()
	return nil
}

func (d *Detector) MaxTokenLen() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.maxToken == 0 {
		return 64
	}
	return d.maxToken
}

func (d *Detector) AnyMatch(s string) bool {
	if d == nil || s == "" {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, p := range d.patterns {
		if p.re != nil && p.re.MatchString(s) {
			return true
		}
	}
	return false
}

func (d *Detector) NewBuilder() *MaskBuilder {
	return &MaskBuilder{
		d:        d,
		counters: map[string]int{},
		mapping:  Mapping{},
		dedupe:   map[string]string{},
		salt:     randHex(4),
	}
}

func (b *MaskBuilder) Mask(s string) string {
	if b == nil || b.d == nil || s == "" {
		return s
	}
	b.d.mu.RLock()
	patterns := append([]Pattern(nil), b.d.patterns...)
	b.d.mu.RUnlock()
	out := s
	for _, p := range patterns {
		out = p.re.ReplaceAllStringFunc(out, func(match string) string {
			key := p.Name + "|" + match
			if existing, ok := b.dedupe[key]; ok {
				return existing
			}
			b.counters[p.Name]++
			placeholder := strings.Replace(p.Mask, "{n}", fmt.Sprintf("%d_%s", b.counters[p.Name], b.salt), 1)
			b.dedupe[key] = placeholder
			b.mapping[placeholder] = match
			return placeholder
		})
	}
	return out
}

func (b *MaskBuilder) Mapping() Mapping { return b.mapping }
func (b *MaskBuilder) Detected() bool   { return len(b.mapping) > 0 }

func Unmask(s string, mapping Mapping) string {
	if s == "" || len(mapping) == 0 {
		return s
	}
	for k, v := range mapping {
		s = strings.ReplaceAll(s, k, v)
	}
	return s
}

func (d *Detector) recomputeMaxLocked() {
	max := 0
	for _, p := range d.patterns {
		l := len(strings.Replace(p.Mask, "{n}", "0123456789", 1))
		if l > max {
			max = l
		}
	}
	d.maxToken = max
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b)
}
