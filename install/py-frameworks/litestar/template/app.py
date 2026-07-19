from litestar import Litestar, get


@get("/")
async def index() -> str:
    return "Hello from Litestar on Jabali."


app = Litestar([index])
