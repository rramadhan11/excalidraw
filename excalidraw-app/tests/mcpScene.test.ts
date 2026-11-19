// The board server's MCP endpoint builds Excalidraw elements in Go
// (excalidraw-server/internal/scene). This checks its output against the
// editor's own code: what the server stores must load unchanged through the
// same restoreElements call that opens a board (boards/useBoardsMode.ts).
//
// The fixture is written by `go test ./internal/scene -update` in
// excalidraw-server; regenerate it whenever the Go builder changes.
import { convertToExcalidrawElements } from "@excalidraw/excalidraw";
import { restoreElements } from "@excalidraw/excalidraw/data/restore";

import type { ExcalidrawElement } from "@excalidraw/element/types";

import fixture from "./fixtures/mcp-scene.json";

type Stored = Record<string, unknown> & { id: string; type: string };

const stored = fixture.elements as unknown as Stored[];
const byId = new Map(stored.map((el) => [el.id, el]));

const restore = () =>
  restoreElements(stored as unknown as ExcalidrawElement[], null, {
    repairBindings: true,
    deleteInvisibleElements: true,
  });

describe("MCP-built scene", () => {
  it("covers every element type the builder makes", () => {
    const types = new Set(stored.map((el) => el.type));
    for (const type of [
      "rectangle",
      "ellipse",
      "diamond",
      "text",
      "line",
      "arrow",
      "frame",
    ]) {
      expect(types).toContain(type);
    }
    expect(stored.some((el) => el.isDeleted)).toBe(true);
  });

  it("restores without dropping, deleting or reordering anything", () => {
    const restored = restore();
    expect(restored.map((el) => el.id)).toEqual(stored.map((el) => el.id));
    expect(restored.map((el) => el.isDeleted)).toEqual(
      stored.map((el) => el.isDeleted),
    );
  });

  it("gets valid, ordered fractional indices on load", () => {
    const indices = restore().map((el) => el.index);
    expect(indices.every((index) => typeof index === "string")).toBe(true);
    expect([...indices].sort()).toEqual(indices);
  });

  it("needs no repair: restore keeps every stored value", () => {
    // The server leaves `index` null; syncInvalidIndices assigns it on load
    // through mutateElement, which also bumps these bookkeeping fields.
    const assignedOnLoad = new Set([
      "index",
      "version",
      "versionNonce",
      "updated",
    ]);
    for (const el of restore()) {
      const original = byId.get(el.id)!;
      for (const [key, value] of Object.entries(original)) {
        if (assignedOnLoad.has(key)) {
          continue;
        }
        expect({ id: el.id, key, value: (el as any)[key] }).toEqual({
          id: el.id,
          key,
          value,
        });
      }
    }
  });

  it("keeps labels and arrow bindings attached", () => {
    const restored = new Map(restore().map((el) => [el.id, el]));
    let labels = 0;
    let bindings = 0;
    for (const el of stored) {
      if (el.isDeleted) {
        continue;
      }
      const containerId = el.containerId as string | null;
      if (containerId) {
        labels++;
        const container = restored.get(containerId)!;
        expect(container.boundElements).toContainEqual({
          type: "text",
          id: el.id,
        });
      }
      for (const key of ["startBinding", "endBinding"] as const) {
        const binding = el[key] as { elementId: string } | null;
        if (!binding) {
          continue;
        }
        bindings++;
        const arrow = restored.get(el.id) as any;
        expect(arrow[key]?.elementId).toBe(binding.elementId);
        expect(restored.get(binding.elementId)!.boundElements).toContainEqual({
          type: "arrow",
          id: el.id,
        });
      }
    }
    expect(labels).toBeGreaterThanOrEqual(4);
    expect(bindings).toBeGreaterThanOrEqual(5);
  });

  it("has every field the editor's own element constructors produce", () => {
    const reference = convertToExcalidrawElements([
      { type: "rectangle", x: 0, y: 0 },
      { type: "ellipse", x: 0, y: 0 },
      { type: "diamond", x: 0, y: 0 },
      { type: "text", x: 0, y: 0, text: "t" },
      { type: "line", x: 0, y: 0 },
      { type: "arrow", x: 0, y: 0 },
      { type: "frame", x: 0, y: 0, children: [] },
    ]);
    for (const ref of reference) {
      // optional in the type and left unset by the constructors
      const expected = Object.keys(ref).filter(
        (key) => (ref as any)[key] !== undefined,
      );
      for (const el of stored.filter((el) => el.type === ref.type)) {
        expect({
          id: el.id,
          missing: expected.filter((k) => !(k in el)),
        }).toEqual({ id: el.id, missing: [] });
      }
    }
  });
});
