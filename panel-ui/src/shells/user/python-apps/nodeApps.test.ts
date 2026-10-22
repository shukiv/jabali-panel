import { describe, expect, it } from "vitest";
import { appRuntimes, createAppBody, nodeEntrypointRe, npmScriptRe, type CreateAppForm } from "./usePythonApps";

// ADR-0175: Node.js apps on the Application Manager. createAppBody builds the
// create request from the fields each runtime has, so a Node.js app never
// carries a Python field, whatever the dialog's form holds.
const filled: CreateAppForm = {
  domain_id: "01DOMAIN",
  name: "api",
  base_uri: "",
  app_root: "apps/api",
  python_version: "3.12",
  app_type: "wsgi",
  entrypoint: "main:app",
  static_url: "/static",
  static_root: "public",
};

describe("createAppBody", () => {
  it("sends a Node.js app without any Python field", () => {
    expect(createAppBody("node", { ...filled, app_type: "node", entrypoint: "server.js" })).toEqual({
      domain_id: "01DOMAIN",
      name: "api",
      runtime: "node",
      app_root: "apps/api",
      base_uri: "/",
      node_version: "22",
      app_type: "node",
      entrypoint: "server.js",
      static_url: "/static",
      static_root: "public",
      media_url: undefined,
      media_root: undefined,
    });
  });

  it("never sends wsgi/asgi as a Node.js app type", () => {
    // The form's initial app_type is wsgi; a Node.js app that never touched
    // the type runs as node.
    expect(createAppBody("node", { ...filled, entrypoint: "server.js" }).app_type).toBe("node");
  });

  it("leaves an npm app's empty script to the server's default (start)", () => {
    const body = createAppBody("node", { ...filled, app_type: "npm", entrypoint: "  " });
    expect(body.app_type).toBe("npm");
    expect(body).not.toHaveProperty("entrypoint");
  });

  it("sends a Python app as before, with runtime python", () => {
    expect(createAppBody("python", filled)).toEqual({
      domain_id: "01DOMAIN",
      name: "api",
      runtime: "python",
      app_root: "apps/api",
      base_uri: "/",
      python_version: "3.12",
      app_type: "wsgi",
      entrypoint: "main:app",
      static_url: "/static",
      static_root: "public",
      media_url: undefined,
      media_root: undefined,
    });
  });

  it("sends a framework install with the slug and no app type, entrypoint or assets", () => {
    expect(createAppBody("python", filled, "django")).toEqual({
      domain_id: "01DOMAIN",
      name: "api",
      runtime: "python",
      app_root: "apps/api",
      base_uri: "/",
      python_version: "3.12",
      framework: "django",
    });
  });
});

describe("appRuntimes", () => {
  it("lists the runtimes that are on, Python first", () => {
    expect(appRuntimes(true, true)).toEqual(["python", "node"]);
    expect(appRuntimes(false, true)).toEqual(["node"]);
    expect(appRuntimes(true, false)).toEqual(["python"]);
    expect(appRuntimes(false, false)).toEqual([]);
  });
});

// The API's rules (panel-api python_apps.go nodeEntrypointRe / npmScriptRe).
describe("Node.js entrypoint rules", () => {
  it("accepts a script file under the app directory", () => {
    for (const ok of ["server.js", "dist/main.mjs", "src/app.cjs", "output/server/index.mjs"]) {
      expect(nodeEntrypointRe.test(ok), ok).toBe(true);
    }
  });

  it("refuses paths that leave the app directory or are not scripts", () => {
    for (const bad of ["../server.js", "/srv/server.js", "a/../b.js", "./server.js", ".hidden.js", "server.ts", "server", "a//b.js"]) {
      expect(nodeEntrypointRe.test(bad), bad).toBe(false);
    }
  });

  it("accepts package.json script names and refuses shell text", () => {
    for (const ok of ["start", "start:prod", "serve.production", "dev-server"]) {
      expect(npmScriptRe.test(ok), ok).toBe(true);
    }
    for (const bad of ["", "-start", "start; rm -rf ~", "start prod", "a".repeat(65)]) {
      expect(npmScriptRe.test(bad), bad).toBe(false);
    }
  });
});
