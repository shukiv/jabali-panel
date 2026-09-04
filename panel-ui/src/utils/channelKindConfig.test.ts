// channelKindConfig.test.ts — invariants tsc can't express for the shared
// notification channel-kind config (JAB-336). The Record<ChannelKind, …> maps
// are completeness-checked by the compiler; these tests pin the behavioural
// contracts around them (which kinds are admin-creatable, conditional-field
// integrity, select options).
import { describe, it, expect } from "vitest";
import {
  CHANNEL_KINDS,
  kindColors,
  kindLabels,
  kindFields,
  type ChannelKind,
} from "./channelKindConfig";

// All kinds in the union (kindLabels is Record<ChannelKind,…>, so its keys are
// exactly the union) — derived, not hard-coded, so it tracks the type.
const ALL_KINDS = Object.keys(kindLabels) as ChannelKind[];

describe("channel-kind palette + labels", () => {
  it("gives every kind a non-empty label and a colour", () => {
    for (const k of ALL_KINDS) {
      expect(kindLabels[k]?.length ?? 0).toBeGreaterThan(0);
      expect(kindColors[k]?.length ?? 0).toBeGreaterThan(0);
    }
  });
});

describe("CHANNEL_KINDS (admin-creatable set)", () => {
  it("lists only real kinds and never duplicates one", () => {
    for (const k of CHANNEL_KINDS) expect(ALL_KINDS).toContain(k);
    expect(new Set(CHANNEL_KINDS).size).toBe(CHANNEL_KINDS.length);
  });

  it("excludes webpush but includes every other kind", () => {
    // webpush lives in the union (existing rows still render) but is managed
    // from the Web Push tab, never created as a channel row.
    expect(CHANNEL_KINDS).not.toContain("webpush");
    for (const k of ALL_KINDS) {
      if (k === "webpush") continue;
      expect(CHANNEL_KINDS).toContain(k);
    }
  });
});

describe("kindFields", () => {
  it("gives every field a name and a label, and select fields their options", () => {
    for (const k of ALL_KINDS) {
      for (const f of kindFields[k]) {
        expect(f.name.length).toBeGreaterThan(0);
        expect(f.label.length).toBeGreaterThan(0);
        if (f.type === "select") {
          expect(f.options?.length ?? 0).toBeGreaterThan(0);
        }
      }
    }
  });

  it("INTEGRITY: every dependsOn points at a field rendered for the same kind", () => {
    // A conditional field whose dependsOn.name isn't in the same kind's field
    // list can never be shown — a silent dead field. (email's external-SMTP
    // fan-out is the real use of this.)
    for (const k of ALL_KINDS) {
      const names = new Set(kindFields[k].map((f) => f.name));
      for (const f of kindFields[k]) {
        if (f.dependsOn) {
          expect(names).toContain(f.dependsOn.name);
        }
      }
    }
  });
});
