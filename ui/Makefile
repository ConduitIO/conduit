.PHONY: dist dependencies lint lint-fix server test

dist:
	@yarn --ignore-optional
	@yarn run build

dependencies:
	@yarn --frozen-lockfile

lint:
	@yarn run lint

lint-fix:
	@yarn run lint:fix

server:
	@yarn run start

test:
	@yarn run test:ember
