"""CLI entry point for jabali-isolator."""

from __future__ import annotations

import asyncio
import json
import logging
import os
import sys

import click

from jabali_isolator import container
from jabali_isolator.config import DEFAULT_CPU, DEFAULT_MEMORY
from jabali_isolator.machine import machine_name

logging.basicConfig(
    format="%(levelname)s: %(message)s",
    level=logging.INFO,
)


def _require_root() -> None:
    if os.geteuid() != 0:
        click.echo("Error: jabali-isolate must be run as root", err=True)
        sys.exit(1)


def _run(coro):
    """Run an async coroutine synchronously."""
    return asyncio.run(coro)


@click.group()
def cli() -> None:
    """jabali-isolate — nspawn container isolation for shell access."""


@cli.command()
@click.argument("user")
@click.option("--memory", default=DEFAULT_MEMORY, help=f"Memory limit (default: {DEFAULT_MEMORY})")
@click.option("--cpu", default=DEFAULT_CPU, help=f"CPU quota (default: {DEFAULT_CPU})")
def create(user: str, memory: str, cpu: str) -> None:
    """Create a container for USER."""
    _require_root()

    if not container.is_available():
        click.echo("Error: systemd-nspawn is not installed", err=True)
        sys.exit(1)

    try:
        result = _run(container.create(user, memory=memory, cpu=cpu))
        click.echo(f"Created container for {user}")
        click.echo(f"  Rootfs:  {result['rootfs']}")
        click.echo(f"  Memory:  {result['memory']}")
        click.echo(f"  CPU:     {result['cpu']}")
        click.echo(f"\nStart with: jabali-isolate start {user}")
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)


@cli.command()
@click.argument("user")
def destroy(user: str) -> None:
    """Destroy a container for USER (stops if running)."""
    _require_root()

    try:
        removed = _run(container.destroy(user))
        if removed:
            click.echo(f"Destroyed container for {user}")
        else:
            click.echo(f"No container found for {user}")
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)


@cli.command()
@click.argument("user")
def start(user: str) -> None:
    """Start a container for USER."""
    _require_root()

    try:
        _run(container.start(user))
        click.echo(f"Started {machine_name(user)}")
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)


@cli.command()
@click.argument("user")
def stop(user: str) -> None:
    """Stop a running container for USER."""
    _require_root()

    try:
        _run(container.stop(user))
        click.echo(f"Stopped {machine_name(user)}")
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)


@cli.command()
@click.argument("user")
def restart(user: str) -> None:
    """Restart a container for USER."""
    _require_root()

    try:
        _run(container.restart(user))
        click.echo(f"Restarted {machine_name(user)}")
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)


@cli.command()
@click.argument("user")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def status(user: str, as_json: bool) -> None:
    """Show status of a container for USER."""
    try:
        info = _run(container.status(user))
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)

    if as_json:
        click.echo(json.dumps(info, indent=2))
    else:
        click.echo(f"User:      {info['user']}")
        click.echo(f"Machine:   {info['machine']}")
        click.echo(f"State:     {info['state']}")
        click.echo(f"Exists:    {info['exists']}")
        click.echo(f"Enabled:   {info['enabled']}")


@cli.command("list")
@click.option("--json", "as_json", is_flag=True, help="Output as JSON")
def list_cmd(as_json: bool) -> None:
    """List all managed containers."""
    try:
        containers = _run(container.list_all())
    except container.IsolatorError as e:
        click.echo(f"Error: {e}", err=True)
        sys.exit(1)

    if as_json:
        click.echo(json.dumps(containers, indent=2))
        return

    if not containers:
        click.echo("No containers found")
        return

    # Table output
    click.echo(f"{'USER':<20} {'MACHINE':<25} {'STATE':<12} {'ENABLED':<8}")
    click.echo("-" * 65)
    for c in containers:
        click.echo(f"{c['user']:<20} {c['machine']:<25} {c['state']:<12} {str(c['enabled']):<8}")
    click.echo(f"\n{len(containers)} container(s)")


if __name__ == "__main__":
    cli()
