from dash import Dash, html

app = Dash(__name__)
app.layout = html.Div("Hello from Dash on Jabali.")

# gunicorn serves the underlying Flask WSGI app (app:server).
server = app.server
