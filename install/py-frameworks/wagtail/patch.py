# Wagtail scaffold hardening (JAB-164). `wagtail start` uses a settings PACKAGE
# (config/settings/{base,dev,production}.py) with a SECRET_KEY baked into base.py,
# so the generic Django hardener (single settings.py) can't express it. Move the
# secret to env in base.py and read ALLOWED_HOSTS + set STATIC_ROOT in
# production.py. Runs AS THE TENANT; the app runs under config.settings.production.
import os
import re

proj = os.environ.get("JAB_PROJECT", "config")
static_root = os.environ.get("JAB_STATIC_ROOT", "staticfiles")

bp = os.path.join(proj, "settings", "base.py")
b = open(bp).read()
if "\nimport os" not in b and not b.startswith("import os"):
    b = "import os\n" + b
if re.search(r"^SECRET_KEY = ", b, flags=re.M):
    b = re.sub(r"^SECRET_KEY = .*$", "SECRET_KEY = os.environ['DJANGO_SECRET_KEY']", b, flags=re.M)
else:
    b = b.rstrip() + "\nSECRET_KEY = os.environ['DJANGO_SECRET_KEY']\n"
open(bp, "w").write(b)

pp = os.path.join(proj, "settings", "production.py")
p = open(pp).read()
if "\nimport os" not in p and not p.startswith("import os"):
    p = "import os\n" + p
if re.search(r"^ALLOWED_HOSTS = ", p, flags=re.M):
    p = re.sub(
        r"^ALLOWED_HOSTS = .*$",
        "ALLOWED_HOSTS = os.environ.get('DJANGO_ALLOWED_HOSTS', '').split(',')",
        p,
        flags=re.M,
    )
else:
    p = p.rstrip() + "\nALLOWED_HOSTS = os.environ.get('DJANGO_ALLOWED_HOSTS', '').split(',')\n"
if "STATIC_ROOT" not in p:
    # BASE_DIR is in scope via `from .base import *`.
    p = p.rstrip() + "\nSTATIC_ROOT = BASE_DIR / %r\n" % static_root
open(pp, "w").write(p)
