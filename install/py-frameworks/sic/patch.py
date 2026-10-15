# sic install (GH #2156). sic reads its settings from two files it leaves to
# the operator: sic/local/secret_settings.py (SECRET_KEY) and
# sic/local/settings_local.py (imported near the end). Write both so every
# value comes from the app's env at run time. sic's settings.py then pins
# CSRF_TRUSTED_ORIGINS to sic.pm's own hosts, after that import, so the app
# runs under sic/local/jabali_settings.py (DJANGO_SETTINGS_MODULE), which
# imports sic's settings and resets it. Then migrate, point the Sites row at
# the domain and make the first admin. Runs AS THE TENANT in app_root; no
# upstream file is changed.
import os
import re
import sys

static_root = os.environ.get("JAB_STATIC_ROOT", "staticfiles")
if not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_-]*", static_root):
    sys.exit("bad static root %r" % static_root)

hosts = [h.strip() for h in os.environ.get("DJANGO_ALLOWED_HOSTS", "").split(",") if h.strip()]
if not hosts:
    sys.exit("DJANGO_ALLOWED_HOSTS is empty: sic needs the domain it serves")
host = hosts[0]

local = os.path.join("sic", "local")
os.makedirs(local, exist_ok=True)

with open(os.path.join(local, "secret_settings.py"), "w") as f:
    f.write(
        "# Written by Jabali: the key lives in the app's env (DJANGO_SECRET_KEY).\n"
        "import os\n"
        "\n"
        "SECRET_KEY = os.environ['DJANGO_SECRET_KEY']\n"
    )

with open(os.path.join(local, "settings_local.py"), "w") as f:
    f.write(
        "# Written by Jabali. Values come from the app's env so an edit there\n"
        "# takes effect on the next restart.\n"
        "import os\n"
        "from pathlib import Path as _Path\n"
        "\n"
        "_env = os.environ.get\n"
        "\n"
        "DEBUG = False\n"
        "ALLOWED_HOSTS = [h.strip() for h in _env('DJANGO_ALLOWED_HOSTS', '').split(',') if h.strip()]\n"
        "STATIC_ROOT = str(_Path(__file__).resolve().parent.parent.parent / %r)\n"
        "\n"
        "EMAIL_BACKEND = 'django.core.mail.backends.smtp.EmailBackend'\n"
        "EMAIL_HOST = _env('SIC_EMAIL_HOST') or 'localhost'\n"
        "EMAIL_PORT = int(_env('SIC_EMAIL_PORT') or '25')\n"
        "EMAIL_HOST_USER = _env('SIC_EMAIL_HOST_USER', '')\n"
        "EMAIL_HOST_PASSWORD = _env('SIC_EMAIL_HOST_PASSWORD', '')\n"
        "EMAIL_USE_TLS = (_env('SIC_EMAIL_USE_TLS') or '').strip().lower() in ('1', 'true', 'yes', 'on')\n"
        "DEFAULT_FROM_EMAIL = _env('SIC_DEFAULT_FROM_EMAIL') or 'noreply@' + (ALLOWED_HOSTS[0] if ALLOWED_HOSTS else 'localhost')\n"
        "SERVER_EMAIL = DEFAULT_FROM_EMAIL\n" % static_root
    )

with open(os.path.join(local, "jabali_settings.py"), "w") as f:
    f.write(
        "# Written by Jabali. sic's settings.py trusts https://*.sic.pm for CSRF\n"
        "# after it imports settings_local; this instance trusts only its own host.\n"
        "from sic.settings import *  # noqa: F401,F403\n"
        "\n"
        "CSRF_TRUSTED_ORIGINS = []\n"
    )

# The script's own directory (app_root) is first on sys.path, so the sic
# package imports from here.
os.environ.setdefault("DJANGO_SETTINGS_MODULE", "sic.local.jabali_settings")
import django  # noqa: E402

django.setup()

from django.conf import settings  # noqa: E402
from django.contrib.sites.models import Site  # noqa: E402
from django.core.exceptions import ValidationError  # noqa: E402
from django.core.management import call_command  # noqa: E402
from django.core.validators import validate_email  # noqa: E402

call_command("migrate", interactive=False, verbosity=0)

Site.objects.update_or_create(pk=settings.SITE_ID, defaults={"domain": host, "name": "sic"})

from sic.models import User  # noqa: E402

if not User.objects.filter(is_admin=True).exists():
    email = (os.environ.get("SIC_ADMIN_EMAIL") or "").strip() or "admin@" + host
    try:
        validate_email(email)
    except ValidationError:
        sys.exit("SIC_ADMIN_EMAIL %r is not an email address" % email)
    password = os.environ.get("SIC_ADMIN_PASSWORD") or ""
    if len(password) < 12:
        sys.exit("SIC_ADMIN_PASSWORD is missing or too short")
    admin = User.objects.create_superuser(email, password=password)
    # sic signs in by email or username, and only a validated email may post.
    admin.username = "admin"
    admin.email_validated = True
    admin.save(update_fields=["username", "email_validated"])
