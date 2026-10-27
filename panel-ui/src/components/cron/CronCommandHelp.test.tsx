// CronCommandHelp.test.tsx — GH #2290 follow-up. The allowed-command help under
// the cron Command field took a lot of room, on mobile above all. It is now a
// "Command help" section, closed until opened, grouped by command type, with
// the shell script forms (bash/sh/the path alone) spelled out.
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { CronCommandHelp } from "./CronCommandHelp";

describe("CronCommandHelp", () => {
  it("is closed until opened, and its header names every command type", () => {
    const { baseElement } = render(<CronCommandHelp />);
    expect(screen.getByText(/Command help/)).toBeTruthy();
    // The header says what's allowed even while closed.
    for (const kind of ["wp", "php", "python", "node", "curl/wget", "shell scripts"]) {
      expect(baseElement.textContent).toContain(kind);
    }
    // The examples stay out of the form until the section is opened.
    expect(baseElement.textContent).not.toContain("bash /home/USER/bin/task.sh");
    expect(baseElement.textContent).not.toContain("php7.4 /usr/local/bin/wp --path=");
  });

  it("opens to sections per command type with examples", () => {
    const { baseElement } = render(<CronCommandHelp />);
    fireEvent.click(screen.getByText(/Command help/));
    for (const heading of ["WP-CLI", "PHP", "Python", "Node.js", "Shell scripts", "HTTP triggers", "Rules for every command"]) {
      expect(screen.getByText(heading)).toBeTruthy();
    }
    const text = baseElement.textContent ?? "";
    // Shell scripts: the three forms, and the SSH condition.
    expect(text).toContain("bash /home/USER/bin/task.sh");
    expect(text).toContain("sh /home/USER/bin/task.sh");
    expect(text).toContain("/home/USER/bin/task.sh arg");
    expect(text).toContain("SSH access");
    // What used to be there stays: WP-CLI on another PHP, and why ls fails.
    expect(text).toContain("php7.4 /usr/local/bin/wp --path=");
    expect(text).toContain("will not work");
  });
});
