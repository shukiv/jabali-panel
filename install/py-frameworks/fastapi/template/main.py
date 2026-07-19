from fastapi import FastAPI

app = FastAPI()


@app.get("/")
def index():
    return {"message": "Hello from FastAPI on Jabali."}
