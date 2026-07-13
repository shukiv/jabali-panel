// JAB-146: monaco-editor's package `exports` map doesn't expose these deep ESM
// subpaths to TypeScript's bundler resolution, so declare them. `editor.api`
// re-exports the full monaco namespace; the `_.contribution` barrel is a
// side-effect import (registers the basic-language grammars).
declare module "monaco-editor/esm/vs/editor/editor.api" {
  export * from "monaco-editor";
}
declare module "monaco-editor/esm/vs/basic-languages/_.contribution";
