package workspace

import (
	"fmt"
	"html"
	"html/template"
	"strings"
	"time"
)

// StarterTSX returns a minimal valid deck used when deck_create is called
// without source. It follows the open-slide file contract so the agent can
// see the preview immediately and then replace it with deck_write.
func StarterTSX(title string, createdAt time.Time) string {
	esc := template.JSEscapeString(title)
	return fmt.Sprintf(`import type { DesignSystem, Page, SlideMeta } from '@open-slide/core';

export const meta: SlideMeta = {
  title: '%s',
  createdAt: '%s',
};

export const design: DesignSystem = {
  palette: { bg: '#0f172a', text: '#f8fafc', accent: '#38bdf8' },
  fonts: {
    display: '"Geist Variable", Geist, system-ui, sans-serif',
    body: '"Geist Variable", Geist, system-ui, sans-serif',
  },
  typeScale: { hero: 140, body: 36 },
  radius: 16,
};

const Cover: Page = () => (
  <div
    style={{
      width: 1920,
      height: 1080,
      display: 'flex',
      flexDirection: 'column',
      justifyContent: 'center',
      padding: 160,
      background: 'var(--osd-bg)',
      color: 'var(--osd-text)',
      fontFamily: 'var(--osd-font-body)',
    }}
  >
    <div style={{ fontFamily: 'var(--osd-font-display)', fontSize: 'var(--osd-hero)', fontWeight: 700, lineHeight: 1.05 }}>
      %s
    </div>
    <div style={{ marginTop: 40, fontSize: 'var(--osd-body)', color: 'var(--osd-accent)' }}>
      This deck was just created. Ask the agent to fill it in.
    </div>
  </div>
);

export default [Cover] satisfies Page[];
`, esc, createdAt.UTC().Format(time.RFC3339), htmlEscape(title))
}

// htmlEscape makes a title safe as JSX text, where braces open expressions.
func htmlEscape(s string) string {
	return strings.NewReplacer("{", "&#123;", "}", "&#125;").Replace(html.EscapeString(s))
}
