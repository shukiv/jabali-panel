# Channels scaffold hardening (JAB-164): SECRET_KEY/ALLOWED_HOSTS from env,
# STATIC_ROOT, register the channels app + ASGI_APPLICATION, and replace asgi.py
# with a ProtocolTypeRouter. Runs AS THE TENANT after `django-admin startproject`.
import os
import re

proj = os.environ.get("JAB_PROJECT", "config")
static_root = os.environ.get("JAB_STATIC_ROOT", "staticfiles")

sp = os.path.join(proj, "settings.py")
s = open(sp).read()
if "\nimport os" not in s and not s.startswith("import os"):
    s = "import os\n" + s
s = re.sub(r"^SECRET_KEY = .*$", "SECRET_KEY = os.environ['DJANGO_SECRET_KEY']", s, flags=re.M)
s = re.sub(
    r"^ALLOWED_HOSTS = .*$",
    "ALLOWED_HOSTS = os.environ.get('DJANGO_ALLOWED_HOSTS', '').split(',')",
    s,
    flags=re.M,
)
if "'channels'" not in s and '"channels"' not in s:
    s = re.sub(r"(INSTALLED_APPS = \[)", r"\1\n    'channels',", s, count=1)
if "STATIC_ROOT" not in s:
    s = s.rstrip() + "\nSTATIC_ROOT = BASE_DIR / %r\n" % static_root
if "ASGI_APPLICATION" not in s:
    s = s.rstrip() + "\nASGI_APPLICATION = %r\n" % (proj + ".asgi.application")
open(sp, "w").write(s)

ap = os.path.join(proj, "asgi.py")
open(ap, "w").write(
    "import os\n"
    "from channels.routing import ProtocolTypeRouter\n"
    "from django.core.asgi import get_asgi_application\n"
    "os.environ.setdefault('DJANGO_SETTINGS_MODULE', %r)\n"
    "django_asgi_app = get_asgi_application()\n"
    "application = ProtocolTypeRouter({'http': django_asgi_app})\n"
    % (proj + ".settings")
)
