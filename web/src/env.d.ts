// Astro's client types (astro:transitions/client and the other virtual
// modules), here so tooling that runs before `astro sync` writes .astro/ (the
// type-aware lint) resolves them too.
/// <reference types="astro/client" />

// The folder picker's attribute, which every browser supports but the HTML
// typings leave out.
declare namespace astroHTML.JSX {
  interface InputHTMLAttributes {
    webkitdirectory?: boolean | undefined | null;
  }
}

// A .astro component imported from a .ts file (components/parity.test.ts
// renders them with the container API). `astro check` types these imports
// through Astro's language server; plain TypeScript and the type-aware lint
// do not know the extension without this.
declare module "*.astro" {
  import type { AstroComponentFactory } from "astro/runtime/server/index.js";
  const Component: AstroComponentFactory;
  export default Component;
}
