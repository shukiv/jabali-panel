// JAB-146: load Monaco's editor CORE (editor.api) plus only the lightweight
// Monarch syntax grammars (basic-languages), NOT the heavy language SERVICES
// (typescript / css / html / json). Importing the full `monaco-editor` barrel
// pulls in those services and their web workers — ts.worker (~6.7 MB), the
// css/html/json workers, and a much larger editor.main — none of which the
// panel uses: MonacoEnvironment (in FileManagerPage) wires ONLY the base
// editor.worker, so the language-service workers were dead weight.
//
// `_.contribution` lazily registers every basic language, so each grammar
// (php / javascript / sql / shell / python / go / rust / java / kotlin / ruby /
// xml / yaml / ini / markdown / css / scss / less / html / typescript-basic …
// see languageByExt) still highlights, loading only on first use. JSON is the
// one exception — its grammar ships only with the dropped json language
// service — so .json files render as plaintext, an acceptable trade for
// dropping several MB from the editor chunk.
import * as monaco from "monaco-editor/esm/vs/editor/editor.api";
import "monaco-editor/esm/vs/basic-languages/_.contribution";

export default monaco;
