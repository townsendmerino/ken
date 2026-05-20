"""Authentication helpers."""

import hashlib


@dataclass
class User:
    name: str
    token: str

    def is_valid(self):
        return bool(self.token)


# validate_user checks a token against a user record.
def validate_user(user, token):
    return user.token == token


@app.route("/login")
async def login(request):
    return {"ok": True}
