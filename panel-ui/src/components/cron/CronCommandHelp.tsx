import { Collapse, Typography } from "antd";
import type { ReactNode } from "react";

// Shared allowed-command help for the cron editors — tenant CreateCronModal and
// admin AdminCreateCronModal render it under the command field. Single source of
// truth for the command restrictions so the two drawers can't drift (they did:
// GH #1686 item 3, where the admin drawer showed no restriction copy at all and
// a tenant/root user had no way to know why `ls` was rejected).
//
// The wording tracks internal/cronvalidate: ValidateCommand's first-token
// allow-list (wp / php / php<X.Y> / python[3][.Y] / node, each running an
// absolute owned script — .php in a docroot, .py/.js under docroots-or-home via
// scriptRoots), shell.go's .sh scripts for an account whose package includes
// SSH (GH #2290), plus the curl/wget self-domain http-trigger route
// (ValidateAny -> ValidateHTTPTrigger, gated on the account's own domains).
//
// GH #2290 follow-up: the help took a lot of room under the field, on mobile
// above all, so it is a "Command help" section, closed until opened, grouped by
// command type. Its header still names every allowed kind.

const Section = ({ title, children }: { title: string; children: ReactNode }) => (
  <div style={{ marginBottom: 8 }}>
    <Typography.Text strong>{title}</Typography.Text>
    <div>
      <Typography.Text type="secondary">{children}</Typography.Text>
    </div>
  </div>
);

const HelpBody = () => (
  <>
    <Section title="WP-CLI">
      <code>wp --path=/home/USER/domains/example.com/public_html cron event run --due-now</code>.{" "}
      <code>--path=</code> is required. <code>wp</code> runs on the account&apos;s CLI
      default PHP; to use another version, start the line with that PHP and
      wp-cli&apos;s path, e.g.{" "}
      <code>php7.4 /usr/local/bin/wp --path=… cron event run --due-now</code>.
    </Section>
    <Section title="PHP">
      <code>php</code> or a pinned version like <code>php8.5</code>, running an
      absolute <code>.php</code> file in one of the account&apos;s docroots:{" "}
      <code>php /home/USER/domains/example.com/public_html/cron.php</code>. Inline
      code (<code>-r</code>) is not allowed.
    </Section>
    <Section title="Python">
      <code>python</code>, <code>python3</code>, <code>python3.X</code> or a
      virtualenv&apos;s interpreter, running an absolute <code>.py</code> file in a
      docroot or the home directory:{" "}
      <code>python3 /home/USER/site/manage.py runjobs</code>. <code>-c</code> and{" "}
      <code>-m</code> are not allowed.
    </Section>
    <Section title="Node.js">
      <code>node</code> (or the absolute path to one), running an absolute{" "}
      <code>.js</code>/<code>.mjs</code>/<code>.cjs</code> file in a docroot or the
      home directory: <code>node /home/USER/app/task.js</code>. Inline code
      (<code>-e</code>, <code>-p</code>) is not allowed.
    </Section>
    <Section title="Shell scripts">
      Only when the account&apos;s hosting plan includes SSH access; root jobs never
      run them. An absolute <code>.sh</code> file in a docroot or the home
      directory: <code>bash /home/USER/bin/task.sh</code>,{" "}
      <code>sh /home/USER/bin/task.sh</code>, or the path alone,{" "}
      <code>/home/USER/bin/task.sh arg</code>, which runs it with bash. No option may
      come before the script. Inside the script, pipes, <code>cd</code> and{" "}
      <code>&amp;&amp;</code> work as usual.
    </Section>
    <Section title="HTTP triggers">
      <code>curl</code> or <code>wget</code>, only to ping one of the
      account&apos;s own domains: <code>curl https://example.com/cron.php</code>.
    </Section>
    <Section title="Rules for every command">
      Use absolute paths instead of <code>cd</code> or <code>$HOME</code>. Shell
      operators (<code>|</code> <code>&amp;</code> <code>$</code> <code>;</code> …)
      are rejected, so plain commands like <code>ls</code>, <code>uptime</code> or{" "}
      <code>cd … &amp;&amp; …</code> will not work. Put one command per line: they
      run in order and stop at the first failure. Lines starting with{" "}
      <code>#</code> are ignored.
    </Section>
  </>
);

export const CronCommandHelp = () => (
  <Collapse
    ghost
    size="small"
    items={[
      {
        key: "help",
        label: (
          <Typography.Text type="secondary">
            Command help: <code>wp</code>, <code>php</code>, <code>python</code>,{" "}
            <code>node</code>, <code>curl/wget</code>, and shell scripts with SSH
            access
          </Typography.Text>
        ),
        children: <HelpBody />,
      },
    ]}
  />
);
