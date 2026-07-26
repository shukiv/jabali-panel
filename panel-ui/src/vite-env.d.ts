/// <reference types="vite/client" />

// Allow side-effectful CSS imports (Refine + AntD reset.css).
declare module "*.css";

// JAB-159: VITE_DEMO="1" selects the demo build; unset/other = production.
interface ImportMetaEnv {
  readonly VITE_DEMO?: string;
}
