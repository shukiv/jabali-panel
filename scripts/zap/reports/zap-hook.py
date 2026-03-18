import logging

def zap_started(zap, target):
    logging.info("Adding session cookie for authenticated scanning")
    zap.replacer.add_rule(
        description="Jabali Session",
        enabled="true",
        matchtype="REQ_HEADER",
        matchregex="false",
        matchstring="Cookie",
        replacement="jabali-session=0518B4Q0KzvvboPhNQ9BgijERH8jDEcbZiC44m59"
    )
    # Trust self-signed certs
    zap.core.set_option_default_user_agent("Mozilla/5.0 ZAP Scan")
