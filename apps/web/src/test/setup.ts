import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

vi.mock("monaco-editor/editor/editor.api", () => ({
  editor: {
    defineTheme: vi.fn(),
    create: vi.fn(() => ({
      getValue: () => "",
      setValue: vi.fn(),
      onDidChangeModelContent: vi.fn(),
      addAction: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  KeyMod: { CtrlCmd: 1 },
  KeyCode: { Enter: 3 },
}));

vi.mock("monaco-editor/languages/definitions/pgsql/register", () => ({}));
vi.mock("monaco-editor/languages/definitions/mysql/register", () => ({}));

afterEach(() => {
  cleanup();
});
